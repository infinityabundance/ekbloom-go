//! Conformance verifier for the **Rust reference** implementation.
//!
//! Reads the implementation-neutral golden set in `conformance/manifest.json`
//! (and every fixture it references) and checks the Rust `ekbloom` crate against
//! it: each file's sha256, the decoded logical bytes (BLAKE3), the machine id,
//! logical length, physical-accounting total, and — for negative fixtures — the
//! SPEC error kind.
//!
//! It deliberately shares no code with the Go verifier and does not import the
//! fixture generator; both verifiers consume the same immutable manifest.
//!
//! The `worst_case_steps` expectation is Go-only: the Rust bound
//! (`AccessPlan::worst_case_steps_for`) has different semantics (positioning
//! only, summed over engine regions, and requires an `Engine` plan), so it is
//! not comparable to the Go `Reader::WorstCaseStepsFor` bound. The count of
//! skipped checks is reported.
//!
//! Usage: `cargo run --release --bin conformance -- <conformance-dir> [--json <path>]`

use std::fs;
use std::path::{Path, PathBuf};
use std::process::ExitCode;

use serde::{Deserialize, Serialize};

use ekbloom::reader::open_bytes;
use ekbloom::{open_bytes_authenticated, read, Dictionary, EkbloomError, ReadRequest};

// ------------------------------------------------------------------ manifest

#[derive(Deserialize)]
struct Manifest {
    format_version: String,
    #[serde(default)]
    authority: String,
    fixtures: Vec<Fixture>,
}

#[derive(Deserialize)]
struct Fixture {
    name: String,
    file: String,
    sha256: String,
    #[serde(default)]
    polarity: String,
    #[serde(default)]
    dict_file: Option<String>,
    #[serde(default)]
    auth_key_hex: Option<String>,
    expected: Expected,
}

#[derive(Deserialize)]
struct Expected {
    #[serde(default)]
    machine_id: Option<u32>,
    #[serde(default)]
    logical_len: Option<u64>,
    #[serde(default)]
    physical_bytes: Option<u64>,
    #[serde(default)]
    blake3_of_decoded: Option<String>,
    #[serde(default)]
    error_kind: Option<String>,
    #[serde(default)]
    error_kind_accept: Option<Vec<String>>,
    #[serde(default)]
    #[allow(dead_code)]
    worst_case_steps: Option<u64>,
}

// ------------------------------------------------------------------ main

fn main() -> ExitCode {
    let mut root: Option<PathBuf> = None;
    let mut json: Option<PathBuf> = None;
    let mut args = std::env::args().skip(1);
    while let Some(a) = args.next() {
        match a.as_str() {
            "--json" => match args.next() {
                Some(p) => json = Some(PathBuf::from(p)),
                None => {
                    eprintln!("usage: conformance <conformance-dir> [--json <path>]");
                    return ExitCode::from(2);
                }
            },
            "-h" | "--help" => {
                println!("usage: conformance <conformance-dir> [--json <path>]");
                return ExitCode::SUCCESS;
            }
            other if root.is_none() => root = Some(PathBuf::from(other)),
            other => {
                eprintln!("unexpected argument: {other}");
                return ExitCode::from(2);
            }
        }
    }
    let root = match root {
        Some(p) => p,
        None => {
            eprintln!("usage: conformance <conformance-dir> [--json <path>]");
            return ExitCode::from(2);
        }
    };
    match run(&root, json.as_deref()) {
        Ok(()) => ExitCode::SUCCESS,
        Err(code) => code,
    }
}

/// Per-fixture observation, published (optionally) so an independent comparator
/// can detect cross-implementation differences without importing either side.
#[derive(Serialize)]
struct FixtureResult {
    name: String,
    polarity: String,
    opened: bool,
    error_kind: Option<String>,
    ok: bool,
}

#[derive(Serialize)]
struct ObservationDocument<'a> {
    implementation: &'a str,
    fixtures: &'a [FixtureResult],
    failures: &'a [String],
}

fn record_failure(failures: &mut Vec<String>, res: &mut FixtureResult, name: &str, msg: String) {
    eprintln!("FAIL {name}: {msg}");
    failures.push(format!("{name}: {msg}"));
    res.ok = false;
}

fn write_json(path: &Path, results: &[FixtureResult], failures: &[String]) -> std::io::Result<()> {
    if let Some(parent) = path.parent() {
        if !parent.as_os_str().is_empty() {
            fs::create_dir_all(parent)?;
        }
    }
    let doc = ObservationDocument {
        implementation: "rust",
        fixtures: results,
        failures,
    };
    let mut out = serde_json::to_vec_pretty(&doc)
        .map_err(|e| std::io::Error::new(std::io::ErrorKind::Other, e))?;
    out.push(b'\n');
    fs::write(path, out)
}

fn run(root: &Path, json_path: Option<&Path>) -> Result<(), ExitCode> {
    let manifest_path = root.join("manifest.json");
    let raw = fs::read(&manifest_path).map_err(|e| {
        eprintln!("read {}: {e}", manifest_path.display());
        ExitCode::FAILURE
    })?;
    let m: Manifest = serde_json::from_slice(&raw).map_err(|e| {
        eprintln!("parse manifest: {e}");
        ExitCode::FAILURE
    })?;

    let mut checks = 0usize;
    let mut skipped = 0usize;
    let mut failures: Vec<String> = Vec::new();
    let mut results: Vec<FixtureResult> = Vec::with_capacity(m.fixtures.len());

    for f in &m.fixtures {
        let mut res = FixtureResult {
            name: f.name.clone(),
            polarity: f.polarity.clone(),
            opened: false,
            error_kind: None,
            ok: true,
        };

        let path = root.join(&f.file);
        let data = match fs::read(&path) {
            Ok(d) => d,
            Err(e) => {
                record_failure(
                    &mut failures,
                    &mut res,
                    &f.name,
                    format!("read {}: {e}", f.file),
                );
                results.push(res);
                continue;
            }
        };
        checks += 1;
        let got_sha = sha256_hex(&data);
        if got_sha != f.sha256 {
            record_failure(
                &mut failures,
                &mut res,
                &f.name,
                format!("sha256 {got_sha} != manifest {}", f.sha256),
            );
            results.push(res);
            continue;
        }

        if f.polarity == "negative" {
            match open_fixture(root, f, &data) {
                Ok(_) => {
                    res.opened = true;
                    record_failure(
                        &mut failures,
                        &mut res,
                        &f.name,
                        "expected rejection but open succeeded".into(),
                    );
                }
                Err(e) => {
                    let got = kind_name(&e);
                    let want = f.expected.error_kind.clone().unwrap_or_default();
                    let accept = f.expected.error_kind_accept.clone().unwrap_or_default();
                    res.error_kind = Some(got.clone());
                    if got != want && !accept.iter().any(|k| *k == got) {
                        record_failure(
                            &mut failures,
                            &mut res,
                            &f.name,
                            format!("rejected with {got}, want {want} (accept {accept:?})"),
                        );
                    }
                    checks += 1;
                }
            }
            results.push(res);
            continue;
        }

        // positive
        let body = match open_fixture(root, f, &data) {
            Ok(b) => {
                res.opened = true;
                b
            }
            Err(e) => {
                res.error_kind = Some(kind_name(&e));
                record_failure(&mut failures, &mut res, &f.name, format!("open: {e}"));
                results.push(res);
                continue;
            }
        };
        if let Some(want) = f.expected.machine_id {
            checks += 1;
            if body.machine_id != want {
                record_failure(
                    &mut failures,
                    &mut res,
                    &f.name,
                    format!("machine_id {} != {want}", body.machine_id),
                );
            }
        }
        if let Some(want) = f.expected.logical_len {
            checks += 1;
            if body.logical_len != want {
                record_failure(
                    &mut failures,
                    &mut res,
                    &f.name,
                    format!("logical_len {} != {want}", body.logical_len),
                );
            }
        }
        if let Some(want) = f.expected.physical_bytes {
            checks += 1;
            let got = body.physical_bytes();
            if got != want {
                record_failure(
                    &mut failures,
                    &mut res,
                    &f.name,
                    format!("physical_bytes {got} != {want}"),
                );
            }
        }
        if let Some(want) = &f.expected.blake3_of_decoded {
            checks += 1;
            let n = body.logical_len;
            match read(
                &body,
                ReadRequest {
                    offset: 0,
                    length: n,
                },
            ) {
                Ok(bytes) => {
                    let got = blake3::hash(&bytes).to_hex().to_string();
                    if got != *want {
                        record_failure(
                            &mut failures,
                            &mut res,
                            &f.name,
                            format!("decoded blake3 {got} != {want}"),
                        );
                    }
                }
                Err(e) => {
                    record_failure(&mut failures, &mut res, &f.name, format!("read: {e}"));
                }
            }
        }
        if f.expected.worst_case_steps.is_some() {
            // Rust's public bound is positioning-only and engine-planned; it is not
            // the same quantity as Go's Reader::WorstCaseStepsFor. Skip, but count.
            skipped += 1;
        }
        results.push(res);
    }

    if let Some(path) = json_path {
        if let Err(e) = write_json(path, &results, &failures) {
            eprintln!("write {}: {e}", path.display());
            return Err(ExitCode::FAILURE);
        }
    }

    println!(
        "conformance(rust): {} — fixtures={} checks={} failures={} skipped={}",
        if failures.is_empty() { "pass" } else { "fail" },
        m.fixtures.len(),
        checks,
        failures.len(),
        skipped
    );
    println!(
        "RESULT rust={} fixtures={} checks={} failures={} skipped={}",
        if failures.is_empty() { "pass" } else { "fail" },
        m.fixtures.len(),
        checks,
        failures.len(),
        skipped
    );
    let _ = m.format_version;
    let _ = m.authority;
    if failures.is_empty() {
        Ok(())
    } else {
        Err(ExitCode::FAILURE)
    }
}

/// Open a fixture through the public crate API, registering a shared dictionary
/// and applying the authenticated entry point when the manifest says so.
fn open_fixture(
    root: &Path,
    f: &Fixture,
    data: &[u8],
) -> Result<ekbloom::EkbloomBody, EkbloomError> {
    if let Some(dict_rel) = &f.dict_file {
        let bytes = fs::read(root.join(dict_rel)).map_err(|e| EkbloomError::InvalidArgument {
            what: format!("read dict {dict_rel}: {e}"),
        })?;
        let d = Dictionary::from_bytes(&bytes)?;
        ekbloom::register_dictionary(&d)?;
    }
    if let Some(hex_key) = &f.auth_key_hex {
        let key = parse_key(hex_key)?;
        return open_bytes_authenticated(data, &key);
    }
    open_bytes(data)
}

fn parse_key(hex_key: &str) -> Result<[u8; 32], EkbloomError> {
    let bytes = unhex(hex_key).ok_or_else(|| EkbloomError::InvalidArgument {
        what: format!("bad auth key hex {hex_key}"),
    })?;
    if bytes.len() != 32 {
        return Err(EkbloomError::InvalidArgument {
            what: format!("auth key must be 32 bytes, got {}", bytes.len()),
        });
    }
    let mut key = [0u8; 32];
    key.copy_from_slice(&bytes);
    Ok(key)
}

/// Map a typed crate error to the stable SPEC taxonomy name. The names mirror the
/// Go sentinels so a single manifest serves both implementations.
fn kind_name(e: &EkbloomError) -> String {
    match e {
        EkbloomError::BadMagic => "ErrBadMagic",
        EkbloomError::UnsupportedVersion { .. } => "ErrUnsupportedVersion",
        EkbloomError::UnsupportedCoreVersion { .. } => "ErrUnsupportedCoreVersion",
        EkbloomError::UnsupportedManifestVersion { .. } => "ErrUnsupportedManifestVersion",
        EkbloomError::Truncated { .. } => "ErrTruncated",
        EkbloomError::ChecksumMismatch { .. } => "ErrChecksumMismatch",
        EkbloomError::Corrupt { .. } => "ErrCorrupt",
        EkbloomError::CorruptGeneration { .. } => "ErrCorrupt",
        EkbloomError::BadSection { .. } => "ErrBadSection",
        EkbloomError::Overflow { .. } => "ErrOverflow",
        EkbloomError::BadDepth { .. } => "ErrBadDepth",
        EkbloomError::BadInterval { .. } => "ErrBadInterval",
        EkbloomError::UnknownMachine { .. } => "ErrUnknownMachine",
        EkbloomError::CoreTooShort { .. } => "ErrCoreTooShort",
        EkbloomError::OutOfBounds { .. } => "ErrOutOfBounds",
        EkbloomError::NotCertified { .. } => "ErrNotCertified",
        EkbloomError::AllocationTooLarge { .. } => "ErrAllocationTooLarge",
        EkbloomError::NonTermination => "ErrNonTermination",
        EkbloomError::InvalidArgument { .. } => "ErrInvalidArgument",
        EkbloomError::RecoveryFailed { .. } => "ErrRecoveryFailed",
        other => return format!("ErrOther({other})"),
    }
    .to_string()
}

// ------------------------------------------------------------------ sha256

const SHA256_K: [u32; 64] = [
    0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
    0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
    0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
    0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
    0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
    0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
    0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
    0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
];

fn sha256(data: &[u8]) -> [u8; 32] {
    let mut h: [u32; 8] = [
        0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab,
        0x5be0cd19,
    ];
    let mut msg = data.to_vec();
    let bitlen = (data.len() as u64).wrapping_mul(8);
    msg.push(0x80);
    while msg.len() % 64 != 56 {
        msg.push(0);
    }
    msg.extend_from_slice(&bitlen.to_be_bytes());

    for block in msg.chunks_exact(64) {
        let mut w = [0u32; 64];
        for i in 0..16 {
            w[i] = u32::from_be_bytes([
                block[i * 4],
                block[i * 4 + 1],
                block[i * 4 + 2],
                block[i * 4 + 3],
            ]);
        }
        for i in 16..64 {
            let s0 = w[i - 15].rotate_right(7) ^ w[i - 15].rotate_right(18) ^ (w[i - 15] >> 3);
            let s1 = w[i - 2].rotate_right(17) ^ w[i - 2].rotate_right(19) ^ (w[i - 2] >> 10);
            w[i] = w[i - 16]
                .wrapping_add(s0)
                .wrapping_add(w[i - 7])
                .wrapping_add(s1);
        }
        let mut v = h;
        for i in 0..64 {
            let s1 = v[4].rotate_right(6) ^ v[4].rotate_right(11) ^ v[4].rotate_right(25);
            let ch = (v[4] & v[5]) ^ ((!v[4]) & v[6]);
            let t1 = v[7]
                .wrapping_add(s1)
                .wrapping_add(ch)
                .wrapping_add(SHA256_K[i])
                .wrapping_add(w[i]);
            let s0 = v[0].rotate_right(2) ^ v[0].rotate_right(13) ^ v[0].rotate_right(22);
            let maj = (v[0] & v[1]) ^ (v[0] & v[2]) ^ (v[1] & v[2]);
            let t2 = s0.wrapping_add(maj);
            v[7] = v[6];
            v[6] = v[5];
            v[5] = v[4];
            v[4] = v[3].wrapping_add(t1);
            v[3] = v[2];
            v[2] = v[1];
            v[1] = v[0];
            v[0] = t1.wrapping_add(t2);
        }
        for i in 0..8 {
            h[i] = h[i].wrapping_add(v[i]);
        }
    }
    let mut out = [0u8; 32];
    for i in 0..8 {
        out[i * 4..i * 4 + 4].copy_from_slice(&h[i].to_be_bytes());
    }
    out
}

fn sha256_hex(data: &[u8]) -> String {
    hex(&sha256(data))
}

fn hex(bytes: &[u8]) -> String {
    let mut s = String::with_capacity(bytes.len() * 2);
    for b in bytes {
        s.push_str(&format!("{b:02x}"));
    }
    s
}

fn unhex(s: &str) -> Option<Vec<u8>> {
    if s.len() % 2 != 0 {
        return None;
    }
    (0..s.len() / 2)
        .map(|i| u8::from_str_radix(&s[i * 2..i * 2 + 2], 16).ok())
        .collect()
}
