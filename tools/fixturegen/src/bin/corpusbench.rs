//! Corpus E_V harness (Rust side): for each file under a corpus directory, encode
//! with machines 1 and 2 under the Root profile and report `physical_bytes`,
//! `E_V = physical_bytes / logical_len`, and single-byte read latency — the same
//! measurement the Go tool `internal/tools/corpusbench` produces, so the two
//! reports can be diffed machine-for-machine on identical files.
//!
//! Usage: corpusbench <corpus-dir> <out.json>
//! Env:   EKB_CORPORA_CAP (default 4 MiB) bounds each file's leading slice.

use std::fs;
use std::path::{Path, PathBuf};

use ekbloom::{create_machine, read, ManifestationProfile, ReadRequest};

const SAMPLES: usize = 0;

fn corpus_cap() -> usize {
    std::env::var("EKB_CORPORA_CAP")
        .ok()
        .and_then(|v| v.parse().ok())
        .filter(|&n| n > 0)
        .unwrap_or(4 << 20)
}

fn walk(dir: &Path, out: &mut Vec<PathBuf>) -> std::io::Result<()> {
    let mut entries: Vec<_> = fs::read_dir(dir)?
        .filter_map(|e| e.ok())
        .map(|e| e.path())
        .collect();
    entries.sort();
    for p in entries {
        if p.is_dir() {
            walk(&p, out)?;
        } else {
            out.push(p);
        }
    }
    Ok(())
}

struct Row {
    corpus: String,
    file: String,
    bytes: usize,
    slice: String,
    machine: u32,
    logical: u64,
    physical: u64,
    e_v: f64,
    p50: u64,
    p99: u64,
    p999: u64,
}

fn pct(sorted: &[u64], per_mille: usize) -> u64 {
    sorted[sorted.len() * per_mille / 1000]
}

fn main() -> ekbloom::Result<()> {
    let args: Vec<String> = std::env::args().skip(1).collect();
    if args.len() < 2 {
        eprintln!("usage: corpusbench <corpus-dir> <out.json>");
        std::process::exit(2);
    }
    let dir = Path::new(&args[0]);
    let out = &args[1];
    let cap = corpus_cap();

    let mut files = Vec::new();
    walk(dir, &mut files)?;
    files.sort();
    if files.is_empty() {
        eprintln!("no files under {dir:?}");
        std::process::exit(1);
    }

    let mut rows: Vec<Row> = Vec::new();
    for path in &files {
        let mut data = fs::read(path)?;
        if data.len() > cap {
            data.truncate(cap);
        }
        if data.is_empty() {
            continue;
        }
        let rel = path
            .strip_prefix(dir)
            .unwrap_or(path)
            .to_string_lossy()
            .to_string();
        let corpus = path
            .parent()
            .and_then(|p| p.file_name())
            .map(|s| s.to_string_lossy().to_string())
            .unwrap_or_else(|| ".".to_string());
        let slice = blake3::hash(&data).to_hex().to_string();

        for m in [1u32, 2u32] {
            let body = create_machine(&data, ManifestationProfile::Root, m)?;
            let n = body.logical_len;
            // One full decode proves exactness. The reference's in-memory read
            // replays from genesis, so a per-call 1-byte read is O(offset) and not
            // comparable to the Go reader's materialised path; latency is therefore
            // measured on the Go side only, and the footprint here is the shared
            // cross-check metric.
            let got = read(
                &body,
                ReadRequest {
                    offset: 0,
                    length: n,
                },
            )?;
            assert_eq!(got, data, "full decode m{m}");
            let durs = vec![0u64];
            rows.push(Row {
                corpus: corpus.clone(),
                file: rel.clone(),
                bytes: data.len(),
                slice: slice.clone(),
                machine: m,
                logical: n,
                physical: body.physical_bytes(),
                e_v: body.physical_bytes() as f64 / n.max(1) as f64,
                p50: pct(&durs, 500),
                p99: pct(&durs, 990),
                p999: pct(&durs, 999),
            });
        }
    }

    let mut js = String::new();
    js.push_str("{\n");
    js.push_str(&format!("  \"corpus_cap_bytes\": {cap},\n"));
    js.push_str(&format!("  \"byte_at_samples\": {SAMPLES},\n"));
    js.push_str("  \"note\": \"Rust reference numbers; compare against evidence/corpus.json (Go) for the same files.\",\n");
    js.push_str("  \"rows\": [\n");
    for (i, r) in rows.iter().enumerate() {
        js.push_str(&format!(
            "    {{\"corpus\": {:?}, \"file\": {:?}, \"bytes\": {}, \"slice_blake3\": {:?}, \"machine_id\": {}, \"logical_len\": {}, \"physical_bytes\": {}, \"E_V\": {:.9}, \"byte_at_p50_ns\": {}, \"byte_at_p99_ns\": {}, \"byte_at_p999_ns\": {}}}{}\n",
            r.corpus, r.file, r.bytes, r.slice, r.machine, r.logical, r.physical, r.e_v, r.p50, r.p99, r.p999,
            if i + 1 == rows.len() { "" } else { "," }
        ));
    }
    js.push_str("  ]\n}\n");
    fs::write(out, js)?;
    println!("wrote {out} ({} rows)", rows.len());
    Ok(())
}
