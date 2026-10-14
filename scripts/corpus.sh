#!/bin/sh
# Standard-corpus E_V study: download the canonical archives, then measure the Go
# implementation (and, when the Rust toolchain is present, the Rust reference) on
# identical files, so `physical_bytes` and `E_V` can be compared machine-for-machine.
#
#   corpus/canterbury, corpus/silesia, corpus/enwik8   (downloaded, git-ignored)
#   evidence/corpus.json         Go numbers
#   evidence/corpus-rust.json    Rust numbers (when available)
#
# Set EKB_CORPORA_CAP to change the per-file slice (default 4 MiB).
# Run from the repository root: sh scripts/corpus.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
mkdir -p corpus evidence

fetch() {
    url=$1
    dest=$2
    if [ -e "$dest" ]; then
        echo "== $dest already present =="
        return
    fi
    echo "== downloading $url =="
    curl -sSL --max-time 600 -o "$dest.zip" "$url"
    mkdir -p "$dest"
    unzip -q -o "$dest.zip" -d "$dest"
    rm -f "$dest.zip"
}

fetch https://corpus.canterbury.ac.nz/resources/cantrbry.zip corpus/canterbury
fetch http://mattmahoney.net/dc/silesia.zip corpus/silesia
fetch http://mattmahoney.net/dc/enwik8.zip corpus/enwik8

# enwik9 (1 GB) is optional: set EKB_CORPUS_ENWIK9=1 to include it. The harness
# measures a bounded leading slice (EKB_CORPORA_CAP, default 4 MiB), so the extra
# corpus is only a download-and-slice cost.
if [ "${EKB_CORPUS_ENWIK9:-0}" = 1 ]; then
    fetch http://mattmahoney.net/dc/enwik9.zip corpus/enwik9
fi

echo
echo "== Go: corpusbench -> evidence/corpus.json =="
go run ./internal/tools/corpusbench corpus evidence/corpus.json

if command -v cargo >/dev/null 2>&1; then
    [ -d .reference/fixturegen ] || sh scripts/setup-reference.sh
    echo
    echo "== Rust: corpusbench -> evidence/corpus-rust.json =="
    ( cd .reference/fixturegen && cargo run --quiet --release --bin corpusbench -- "$root/corpus" "$root/evidence/corpus-rust.json" )
    echo
    echo "== compare physical_bytes / E_V (must be identical) =="
    go run ./internal/tools/corpuscompare evidence/corpus.json evidence/corpus-rust.json | tee evidence/corpus-compare.log
else
    echo "note: Rust toolchain not present; skipping the Rust comparison"
fi
