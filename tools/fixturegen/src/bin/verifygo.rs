//! Verify `.ekb` files written by the Go implementation: open each with the Rust
//! reference, decode every symbol through the file-backed reader, and compare to
//! the sibling `.src` file. Prints a per-file line and exits nonzero on any
//! mismatch.
//!
//! Usage: cargo run --bin verifygo -- <dir>

use std::fs;
use std::fs::File;
use std::path::Path;
use std::process::ExitCode;

use ekbloom::{inspect_file, open, read_file, ReadRequest, StreamReader};

fn main() -> ExitCode {
    let mut args = std::env::args().skip(1);
    let dir = match args.next() {
        Some(d) => d,
        None => {
            eprintln!("usage: verifygo <dir>");
            return ExitCode::from(2);
        }
    };
    let dir = Path::new(&dir);
    let mut entries: Vec<_> = match fs::read_dir(dir) {
        Ok(rd) => rd.filter_map(|e| e.ok()).map(|e| e.path()).collect(),
        Err(e) => {
            eprintln!("read_dir {dir:?}: {e}");
            return ExitCode::FAILURE;
        }
    };
    entries.sort();

    let mut checked = 0usize;
    let mut failed = 0usize;
    for ekb in entries
        .iter()
        .filter(|p| p.extension().is_some_and(|e| e == "ekb"))
    {
        let name = ekb.file_stem().unwrap().to_string_lossy().to_string();
        let src_path = dir.join(format!("{name}.src"));
        let src = match fs::read(&src_path) {
            Ok(s) => s,
            Err(e) => {
                eprintln!("{name}: read src: {e}");
                failed += 1;
                continue;
            }
        };
        // A machine-9 body that *references* a shared dictionary carries the
        // dictionary out-of-band; register it (as the writer's registry does)
        // before opening so the core's dictionary id resolves.
        let dict_path = dir.join(format!("{name}.dict"));
        if let Ok(dict_bytes) = fs::read(&dict_path) {
            match ekbloom::Dictionary::from_bytes(&dict_bytes) {
                Ok(d) => {
                    if let Err(e) = ekbloom::register_dictionary(&d) {
                        eprintln!("{name}: register dictionary: {e}");
                        failed += 1;
                        continue;
                    }
                }
                Err(e) => {
                    eprintln!("{name}: parse dictionary: {e}");
                    failed += 1;
                    continue;
                }
            }
        }

        let body = match open(ekb) {
            Ok(b) => b,
            Err(e) => {
                eprintln!("{name}: open: {e}");
                failed += 1;
                continue;
            }
        };
        let n = body.logical_len;
        let bytes = match read_file(
            ekb,
            ReadRequest {
                offset: 0,
                length: n,
            },
        ) {
            Ok(b) => b,
            Err(e) => {
                eprintln!("{name}: read: {e}");
                failed += 1;
                continue;
            }
        };
        if bytes != src {
            eprintln!(
                "{name}: decode mismatch ({} vs {} bytes)",
                bytes.len(),
                src.len()
            );
            failed += 1;
            continue;
        }
        if name.contains("auth") {
            let key: [u8; 32] = std::array::from_fn(|i| i as u8);
            match ekbloom::open_authenticated(ekb, &key) {
                Ok(_) => {}
                Err(e) => {
                    eprintln!("{name}: authenticated open failed: {e}");
                    failed += 1;
                    continue;
                }
            }
        }
        match inspect_file(ekb) {
            Ok(report) => {
                println!(
                    "{name}: OK n={n} visible={} physical={}",
                    report.visible_bytes, report.physical_bytes
                );
            }
            Err(e) => {
                eprintln!("{name}: inspect: {e}");
                failed += 1;
                continue;
            }
        }
        checked += 1;
    }

    // Verify Go-written stream containers with the Rust StreamReader.
    for stream in entries
        .iter()
        .filter(|p| p.extension().is_some_and(|e| e == "stream"))
    {
        let name = stream.file_stem().unwrap().to_string_lossy().to_string();
        let src_path = dir.join(format!("{name}.src"));
        let src = match fs::read(&src_path) {
            Ok(s) => s,
            Err(e) => {
                eprintln!("{name}: read src: {e}");
                failed += 1;
                continue;
            }
        };
        let file = match File::open(stream) {
            Ok(f) => f,
            Err(e) => {
                eprintln!("{name}: open: {e}");
                failed += 1;
                continue;
            }
        };
        let reader = match StreamReader::open(file) {
            Ok(r) => r,
            Err(e) => {
                eprintln!("{name}: stream open: {e}");
                failed += 1;
                continue;
            }
        };
        let n = reader.len() as usize;
        let mut buf = vec![0u8; n];
        if let Err(e) = reader.read_at(0, &mut buf) {
            eprintln!("{name}: stream read: {e}");
            failed += 1;
            continue;
        }
        if buf != src {
            eprintln!("{name}: stream decode mismatch");
            failed += 1;
            continue;
        }
        println!("{name}: OK (stream) n={n}");
        checked += 1;
    }
    println!("verified {checked} file(s), {failed} failure(s)");
    if failed > 0 {
        ExitCode::FAILURE
    } else {
        ExitCode::SUCCESS
    }
}
