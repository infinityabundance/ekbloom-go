#!/bin/sh
# Materialise the Rust reference and the committed fixture generator under
# .reference/ so scripts/roundtrip.sh can run from a fresh clone.
#
#   - clones github.com/infinityabundance/EKBLOOM (Rust) into .reference/ekbloom-rust
#     at a pinned commit;
#   - copies the committed tools/fixturegen crate into .reference/fixturegen.
#
# Idempotent. Requires git and network on first run. Override the source with
# EKBLOOM_RUST_REPO / EKBLOOM_RUST_COMMIT.
#
# Run from the repository root: sh scripts/setup-reference.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

REF_REPO=${EKBLOOM_RUST_REPO:-https://github.com/infinityabundance/EKBLOOM.git}
REF_COMMIT=${EKBLOOM_RUST_COMMIT:-278f9215a6cc481821208e1daa7614574d34bcfc}

if [ ! -f .reference/ekbloom-rust/Cargo.toml ]; then
    echo "== cloning the Rust reference -> .reference/ekbloom-rust =="
    mkdir -p .reference
    rm -rf .reference/ekbloom-rust
    git clone --quiet "$REF_REPO" .reference/ekbloom-rust
    ( cd .reference/ekbloom-rust && git --no-pager checkout --quiet "$REF_COMMIT" )
fi

echo "== staging tools/fixturegen -> .reference/fixturegen =="
rm -rf .reference/fixturegen
mkdir -p .reference/fixturegen
cp -r tools/fixturegen/. .reference/fixturegen/

echo "reference ready: .reference/ekbloom-rust @ $REF_COMMIT"
