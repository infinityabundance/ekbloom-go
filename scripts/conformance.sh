#!/bin/sh
# Conformance suite: verify BOTH implementations against the immutable,
# implementation-neutral golden set in conformance/manifest.json, then enforce
# the ruled divergence set with an independent comparator.
#
# The manifest and its fixtures are the authority (conformance/SPEC.md); neither
# implementation generates them at test time. This runner:
#   - runs the Go verifier (conformance/verify/go) inside the OOM-capped sandbox,
#     capturing per-fixture observations (-json);
#   - if the Rust reference is staged under .reference/, runs the Rust verifier
#     (tools/fixturegen/src/bin/conformance.rs) in a capped Rust container, also
#     capturing per-fixture observations (--json);
#   - runs the independent comparator (conformance/tools/divergence, which
#     imports neither implementation) over the manifest, SPEC.md, and both
#     observation files; it exits non-zero on an unruled divergence, a ruled
#     divergence without a SPEC-citing rule, or a manifest inconsistency;
#   - writes evidence/conformance.json and evidence/conformance.log.
#
# Nothing heavy runs on the host: go and cargo both run inside capped Docker
# containers. Runnable from the repository root:
#
#   sh scripts/conformance.sh
#
# Tunables: CONFORMANCE_RUST_IMAGE to pin the Rust toolchain image.
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

conf="$root/conformance"
evid="$root/evidence"
mkdir -p "$evid"
log="$evid/conformance.log"
: > "$log"

go_obs="$evid/conformance-go.json"
rust_obs="$evid/conformance-rust.json"
div_summary="$evid/divergence.json"

fixtures=$(grep -c '"name":' "$conf/manifest.json" 2>/dev/null || true)
[ -n "$fixtures" ] || fixtures=0

log() { printf '%s\n' "$*" | tee -a "$log"; }

# --- Go implementation (OOM-capped sandbox) --------------------------------
log "== conformance: Go verifier (sandbox, capped) =="
go_status=fail
rm -f "$go_obs"
if sh scripts/sandbox.sh sh -c \
    'cd /src/conformance/verify/go && export GOFLAGS="-mod=mod -buildvcs=false" && go run . -root /src/conformance -json /src/evidence/conformance-go.json' \
    >>"$log" 2>&1; then
    go_status=pass
fi

# --- Rust reference (capped container), only if staged ---------------------
rust_status=skipped
rm -f "$rust_obs"
if [ -f "$root/.reference/ekbloom-rust/Cargo.toml" ]; then
    # Re-stage the committed fixturegen sources (idempotent; skips the clone).
    sh scripts/setup-reference.sh >/dev/null 2>&1 || true

    image=${CONFORMANCE_RUST_IMAGE:-}
    if [ -z "$image" ]; then
        for cand in ekbloom-rust:1.99.0 rust:1.99-slim-bookworm rust:1.99-bookworm rust:1-slim-bookworm; do
            if docker image inspect "$cand" >/dev/null 2>&1; then image=$cand; break; fi
        done
    fi
    if [ -z "$image" ]; then
        for cand in rust:1.99-slim-bookworm rust:1-slim-bookworm; do
            if docker pull "$cand" >/dev/null 2>&1; then image=$cand; break; fi
        done
    fi

    if [ -z "$image" ]; then
        log "rust: no Rust toolchain image available; skipped"
        rust_status=skipped
    else
        log "== conformance: Rust verifier ($image, capped) =="
        # A persistent cargo registry + target cache keeps reruns fast; run as
        # root so the named volumes are writable regardless of the image's USER.
        if docker run --rm --user 0 \
            --memory=2g --memory-swap=2g --cpus=2 --pids-limit=512 --ulimit nofile=4096:4096 \
            -v "$root":/src -w /src/.reference/fixturegen \
            -v ekbloom-cargo-registry:/usr/local/cargo/registry \
            -v ekbloom-conformance-cargo:/ctarget \
            -e CARGO_TARGET_DIR=/ctarget \
            "$image" sh -c \
            'cargo build --release --bin conformance && /ctarget/release/conformance /src/conformance --json /src/evidence/conformance-rust.json' \
            >>"$log" 2>&1; then
            rust_status=pass
        else
            rust_status=fail
        fi
    fi
else
    log "rust: reference not staged (.reference/ekbloom-rust absent); skipped"
fi

# --- Independent comparator (OOM-capped sandbox) ---------------------------
log "== conformance: independent divergence comparator (sandbox, capped) =="
div_status=fail
rm -f "$div_summary"
rust_arg=""
if [ "$rust_status" != skipped ]; then
    rust_arg="-rust /src/evidence/conformance-rust.json"
fi
if sh scripts/sandbox.sh sh -c \
    "cd /src/conformance/tools/divergence && export GOFLAGS='-mod=mod -buildvcs=false' && go run . -manifest /src/conformance/manifest.json -spec /src/conformance/SPEC.md -go /src/evidence/conformance-go.json $rust_arg -json /src/evidence/divergence.json" \
    >>"$log" 2>&1; then
    div_status=pass
fi

# --- Summarise -------------------------------------------------------------
# Extract the comparator's counts from its machine-readable summary. Each field
# lands on its own line (MarshalIndent), so a targeted grep is enough.
jnum() {
    [ -f "$div_summary" ] || { printf '0'; return; }
    v=$(grep -oE "\"$1\":[[:space:]]*[0-9]+" "$div_summary" | grep -oE '[0-9]+' | head -n1)
    [ -n "$v" ] || v=0
    printf '%s' "$v"
}
div_ruled=$(jnum ruled)
div_observed=$(jnum observed)
div_unruled=$(jnum unruled)
div_resolved=$(jnum resolved)
div_norule=$(jnum ruled_without_rule)

cat > "$evid/conformance.json" <<EOF
{"go":"$go_status","rust":"$rust_status","fixtures":$fixtures,"divergences":{"ruled":$div_ruled,"observed":$div_observed,"unruled":$div_unruled,"resolved":$div_resolved,"ruled_without_rule":$div_norule,"source":"manifest.json+SPEC.md"}}
EOF

log "wrote $evid/conformance.json:"
cat "$evid/conformance.json" | tee -a "$log"

if [ "$go_status" != pass ]; then
    exit 1
fi
if [ "$rust_status" = fail ]; then
    exit 1
fi
if [ "$div_status" != pass ]; then
    log "conformance: unruled divergence or manifest inconsistency (see $div_summary / $log)"
    exit 1
fi
exit 0
