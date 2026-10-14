//! Fixture generator: writes ground-truth `.ekb` files and BLAKE3 vectors from the
//! EKBLOOM reference implementation (the Rust crate in `../ekbloom-rust`) so the
//! Go port can prove byte-for-byte format identity.
//!
//! Run from this directory: `cargo run --release`. Output lands in
//! `<repo>/testdata/` (relative to this crate: `../../testdata`).

use std::fs;
use std::path::{Path, PathBuf};

use ekbloom::{
    create_embedded_with_dict, create_machine, create_with_dict, inspect, inspect_file,
    train_dictionary, write_body, write_body_authenticated, Dictionary, ManifestationProfile,
    RepresentationPolicy, StreamBuilder, StreamOptions,
};
use std::fs::File;

fn lcg(seed: u64, len: usize) -> Vec<u8> {
    let mut s = seed;
    let mut out = Vec::with_capacity(len);
    for _ in 0..len {
        s = s
            .wrapping_mul(6364136223846793005)
            .wrapping_add(1442695040888963407);
        out.push((s >> 33) as u8);
    }
    out
}

/// English-like 4-symbol skew (60/25/10/5).
fn skewed(seed: u64, len: usize) -> Vec<u8> {
    let mut s = seed;
    let alpha = *b"abcd";
    let mut out = Vec::with_capacity(len);
    for _ in 0..len {
        s = s
            .wrapping_mul(6364136223846793005)
            .wrapping_add(1442695040888963407);
        let r = (s >> 33) % 100;
        let idx = if r < 60 {
            0
        } else if r < 85 {
            1
        } else if r < 95 {
            2
        } else {
            3
        };
        out.push(alpha[idx]);
    }
    out
}

fn text(len: usize) -> Vec<u8> {
    let base = b"the quick brown fox jumps over 0123456789 lazy dog; ";
    let mut out = Vec::with_capacity(len);
    while out.len() < len {
        for &b in base {
            if out.len() >= len {
                break;
            }
            out.push(b);
        }
    }
    out
}

fn constant(b: u8, len: usize) -> Vec<u8> {
    vec![b; len]
}

/// A binary mixed source: constant regions interleaved with pseudorandom regions.
fn mixed(len: usize) -> Vec<u8> {
    let rnd = lcg(0xABCDEF, len);
    let mut out = Vec::with_capacity(len);
    for i in 0..len {
        if (i / 512) % 3 == 0 {
            out.push(0x7F);
        } else {
            out.push(rnd[i]);
        }
    }
    out
}

fn hex(bytes: &[u8]) -> String {
    let mut s = String::with_capacity(bytes.len() * 2);
    for b in bytes {
        s.push_str(&format!("{b:02x}"));
    }
    s
}

/// Deterministic pattern used for the BLAKE3 vectors: `data[i] = (i*7 + 11) & 0xff`.
fn pattern(len: usize) -> Vec<u8> {
    (0..len)
        .map(|i| ((i as u64 * 7 + 11) & 0xff) as u8)
        .collect()
}

struct Fixture {
    name: &'static str,
    machine_id: u32,
    profile: &'static str,
    source: Vec<u8>,
    dict: Option<Vec<u8>>,
}

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let out_root: PathBuf = PathBuf::from("../../testdata");
    let fx_dir = out_root.join("fixtures");
    fs::create_dir_all(&fx_dir)?;

    // ---- BLAKE3 vectors -------------------------------------------------
    let key: [u8; 32] = std::array::from_fn(|i| i as u8);
    let lens = [
        0usize, 1, 2, 63, 64, 65, 127, 128, 255, 256, 1023, 1024, 1025, 2048, 3072, 4096, 5000,
        8192, 10000, 100000,
    ];
    let mut blake3_vecs =
        String::from("{\n  \"pattern\": \"data[i] = (i*7 + 11) & 0xff\",\n  \"key_hex\": \"");
    blake3_vecs.push_str(&hex(&key));
    blake3_vecs.push_str("\",\n  \"vectors\": [\n");
    for (i, &len) in lens.iter().enumerate() {
        let data = pattern(len);
        let h = blake3::hash(&data);
        let kh = blake3::keyed_hash(&key, &data);
        blake3_vecs.push_str(&format!(
            "    {{\"len\": {len}, \"hash\": \"{}\", \"keyed\": \"{}\"}}{}\n",
            h.to_hex(),
            kh.to_hex(),
            if i + 1 == lens.len() { "" } else { "," }
        ));
    }
    blake3_vecs.push_str("  ]\n}\n");
    fs::write(out_root.join("blake3_vectors.json"), blake3_vecs)?;

    // ---- Fixtures -------------------------------------------------------
    let mut fixtures: Vec<Fixture> = Vec::new();
    fixtures.push(Fixture {
        name: "m1_root_empty",
        machine_id: 1,
        profile: "root",
        source: Vec::new(),
        dict: None,
    });
    fixtures.push(Fixture {
        name: "m1_root_allbytes",
        machine_id: 1,
        profile: "root",
        source: (0u8..=255).collect(),
        dict: None,
    });
    fixtures.push(Fixture {
        name: "m1_root_text",
        machine_id: 1,
        profile: "root",
        source: text(100),
        dict: None,
    });
    fixtures.push(Fixture {
        name: "m1_full_rand",
        machine_id: 1,
        profile: "full",
        source: lcg(1, 4096),
        dict: None,
    });
    fixtures.push(Fixture {
        name: "m1_sparse64_text",
        machine_id: 1,
        profile: "sparse(64)",
        source: text(1000),
        dict: None,
    });
    fixtures.push(Fixture {
        name: "m1_path4_rand",
        machine_id: 1,
        profile: "path(4)",
        source: lcg(2, 4096),
        dict: None,
    });
    fixtures.push(Fixture {
        name: "m2_root_skew",
        machine_id: 2,
        profile: "root",
        source: skewed(3, 5000),
        dict: None,
    });
    fixtures.push(Fixture {
        name: "m2_sparse256_skew",
        machine_id: 2,
        profile: "sparse(256)",
        source: skewed(3, 5000),
        dict: None,
    });
    fixtures.push(Fixture {
        name: "m3_root_rand",
        machine_id: 3,
        profile: "root",
        source: lcg(4, 4096),
        dict: None,
    });
    fixtures.push(Fixture {
        name: "m4_root_skew",
        machine_id: 4,
        profile: "root",
        source: skewed(5, 5000),
        dict: None,
    });
    fixtures.push(Fixture {
        name: "m5_root_skew",
        machine_id: 5,
        profile: "root",
        source: skewed(6, 5000),
        dict: None,
    });
    fixtures.push(Fixture {
        name: "m5_root_const",
        machine_id: 5,
        profile: "root",
        source: constant(0x41, 500),
        dict: None,
    });
    fixtures.push(Fixture {
        name: "m6_root_mixed",
        machine_id: 6,
        profile: "root",
        source: mixed(8192),
        dict: None,
    });
    fixtures.push(Fixture {
        name: "m6_root_const",
        machine_id: 6,
        profile: "root",
        source: constant(0x00, 9000),
        dict: None,
    });
    fixtures.push(Fixture {
        name: "m7_root_skew",
        machine_id: 7,
        profile: "root",
        source: skewed(7, 5000),
        dict: None,
    });
    fixtures.push(Fixture {
        name: "m8_root_skew",
        machine_id: 8,
        profile: "root",
        source: skewed(8, 5000),
        dict: None,
    });
    fixtures.push(Fixture {
        name: "m9_root_small",
        machine_id: 9,
        profile: "root",
        source: b"user=alice|region=us-east-1|role=admin".to_vec(),
        dict: None,
    });
    fixtures.push(Fixture {
        name: "m9_full_small",
        machine_id: 9,
        profile: "full",
        source: b"user=alice|region=us-east-1|role=admin".to_vec(),
        dict: None,
    });

    let mut fixture_json = String::new();
    fixture_json.push_str("{\n  \"fixtures\": [\n");
    let mut entries: Vec<String> = Vec::new();

    for fx in &fixtures {
        let ekb_path = fx_dir.join(format!("{}.ekb", fx.name));
        let src_path = fx_dir.join(format!("{}.src", fx.name));
        let profile = parse_profile(fx.profile);

        let body = create_machine(&fx.source, profile, fx.machine_id)?;
        let report = inspect(&body);
        write_body(&ekb_path, &body)?;
        let file_report = inspect_file(&ekb_path)?;
        assert_eq!(
            file_report.physical_bytes, report.physical_bytes,
            "inspect_file disagrees with inspect for {}",
            fx.name
        );
        assert_eq!(file_report.visible_bytes, report.visible_bytes);
        fs::write(&src_path, &fx.source)?;

        // Reference decode the whole object through the body and assert it equals
        // the source (catches any machine-encode/decode surprise).
        for (i, &expect) in fx.source.iter().enumerate() {
            let got = body.symbol_at(i as u64)?;
            assert_eq!(
                got, expect,
                "body.symbol_at mismatch for {} at {i}",
                fx.name
            );
        }

        let dict_field = write_dict_if_any(&fx_dir, fx.name, &fx.dict)?;
        let machine = ekbloom::machine_by_id(fx.machine_id).expect("machine");
        let reencoded = machine.encode(&fx.source)?;
        let encode_matches = reencoded == body.core;
        entries.push(format!(
            "    {{\"name\": \"{}\", \"machine_id\": {}, \"profile\": \"{}\", \"logical_len\": {}, \"core_len\": {}, \"visible_bytes\": {}, \"physical_bytes\": {}, \"generation\": {}, \"encode_matches\": {}, \"dict\": {}}}",
            fx.name,
            fx.machine_id,
            fx.profile,
            fx.source.len(),
            body.core.len(),
            report.visible_bytes,
            report.physical_bytes,
            body.generation,
            encode_matches,
            dict_field
        ));
    }

    // Referenced-dictionary fixture (Go must register the dictionary to read it).
    {
        let corpus: [&[u8]; 3] = [
            b"user=alice|region=us-east-1|role=admin",
            b"user=bob|region=eu-west-1|role=reader",
            b"user=carol|region=us-west-2|role=writer",
        ];
        let dict: Dictionary = train_dictionary(&corpus)?;
        let record: &[u8] = b"user=dave|region=ap-south-1|role=admin";
        let body = create_with_dict(&dict, record)?;
        let report = inspect(&body);
        let name = "m9_ref_kv";
        write_body(&fx_dir.join(format!("{name}.ekb")), &body)?;
        fs::write(fx_dir.join(format!("{name}.src")), record)?;
        let dict_bytes = dict.to_bytes();
        fs::write(fx_dir.join(format!("{name}.dict")), &dict_bytes)?;
        entries.push(format!(
            "    {{\"name\": \"{name}\", \"machine_id\": 9, \"profile\": \"root\", \"logical_len\": {}, \"core_len\": {}, \"visible_bytes\": {}, \"physical_bytes\": {}, \"generation\": {}, \"encode_matches\": false, \"dict\": \"fixtures/{name}.dict\"}}",
            record.len(),
            body.core.len(),
            report.visible_bytes,
            report.physical_bytes,
            body.generation
        ));
    }

    // Embedded-dictionary fixture (self-contained; no registry needed).
    {
        let corpus: [&[u8]; 3] = [
            b"user=alice|region=us-east-1|role=admin",
            b"user=bob|region=eu-west-1|role=reader",
            b"user=carol|region=us-west-2|role=writer",
        ];
        let dict: Dictionary = train_dictionary(&corpus)?;
        let record: &[u8] = b"user=erin|region=eu-central-1|role=writer";
        let body = create_embedded_with_dict(record, ManifestationProfile::Root, &dict)?;
        let report = inspect(&body);
        let name = "m9_embed_kv";
        write_body(&fx_dir.join(format!("{name}.ekb")), &body)?;
        fs::write(fx_dir.join(format!("{name}.src")), record)?;
        entries.push(format!(
            "    {{\"name\": \"{name}\", \"machine_id\": 9, \"profile\": \"root\", \"logical_len\": {}, \"core_len\": {}, \"visible_bytes\": {}, \"physical_bytes\": {}, \"generation\": {}, \"encode_matches\": false, \"dict\": null}}",
            record.len(),
            body.core.len(),
            report.visible_bytes,
            report.physical_bytes,
            body.generation
        ));
    }

    // ---- Authenticated fixture (Rust writes, Go verifies) --------------
    {
        let key: [u8; 32] = std::array::from_fn(|i| i as u8);
        let src = text(300);
        let body = create_machine(&src, ManifestationProfile::Full, ekbloom::MACHINE_NL2)?;
        let auth_dir = out_root.join("auth");
        fs::create_dir_all(&auth_dir)?;
        write_body_authenticated(&auth_dir.join("golden_auth.ekb"), &body, &key)?;
        fs::write(auth_dir.join("golden_auth.src"), &src)?;
        eprintln!("wrote authenticated golden fixture");
    }

    fixture_json.push_str(&entries.join(",\n"));
    fixture_json.push_str("\n  ]\n}\n");
    fs::write(out_root.join("fixtures.json"), fixture_json)?;

    // ---- Streaming container golden file -------------------------------
    {
        let stream_dir = out_root.join("stream");
        fs::create_dir_all(&stream_dir)?;
        let mut source = text(9000);
        source.extend_from_slice(&lcg(0x5157, 9000));
        let path = stream_dir.join("golden.stream");
        let f = File::create(&path)?;
        let opts = StreamOptions {
            block_bytes: 4096,
            machine: RepresentationPolicy::Auto,
        };
        let mut builder = StreamBuilder::new(f, opts)?;
        builder.write(&source[..5000])?;
        builder.checkpoint()?;
        builder.write(&source[5000..])?;
        let manifest = builder.finish()?;
        fs::write(stream_dir.join("golden.src"), &source)?;
        fs::write(
            out_root.join("stream.json"),
            format!(
                "{{\"logical_len\": {}, \"block_bytes\": {}, \"block_count\": {}, \"container_bytes\": {}, \"payload_bytes\": {}, \"source_hash\": \"{}\"}}\n",
                manifest.logical_len,
                manifest.block_bytes,
                manifest.block_count,
                manifest.container_bytes,
                manifest.payload_bytes,
                hex(&manifest.source_hash)
            ),
        )?;
        eprintln!(
            "wrote stream golden: {} logical bytes, {} blocks",
            manifest.logical_len, manifest.block_count
        );
    }

    eprintln!(
        "wrote {} fixtures and {} blake3 vectors to {}",
        entries.len(),
        lens.len(),
        out_root.display()
    );
    Ok(())
}

fn write_dict_if_any(
    dir: &Path,
    name: &str,
    dict: &Option<Vec<u8>>,
) -> Result<String, Box<dyn std::error::Error>> {
    match dict {
        Some(bytes) => {
            let p = dir.join(format!("{name}.dict"));
            fs::write(&p, bytes)?;
            Ok(format!("\"fixtures/{name}.dict\""))
        }
        None => Ok("null".to_string()),
    }
}

fn parse_profile(label: &str) -> ManifestationProfile {
    match label {
        "root" => ManifestationProfile::Root,
        "full" => ManifestationProfile::Full,
        "sparse(64)" => ManifestationProfile::Sparse { interval: 64 },
        "sparse(256)" => ManifestationProfile::Sparse { interval: 256 },
        "path(4)" => ManifestationProfile::Path { depth: 4 },
        other => panic!("unknown profile label {other}"),
    }
}
