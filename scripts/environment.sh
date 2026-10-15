#!/bin/sh
# Generate evidence/environment.txt from the environment that ACTUALLY produced
# the evidence — never by hand.
#
# Every check runs INSIDE scripts/sandbox.sh (the OOM-capped container), so the
# file records the container's go version, the GOMAXPROCS pin, the caps, the
# kernel, the host nproc, the cross-compile matrix, the module's dependency count
# and cgo/unsafe status, and the go vet / go test counts observed for this commit.
# Because it is generated from a real run, it cannot go stale.
#
# Run from the repository root: sh scripts/environment.sh [out]
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

out=${1:-evidence/environment.txt}
mkdir -p evidence

# sandbox.sh execs `docker run ... "$@"`; pass the desired output path (relative to
# the repo root, i.e. /src inside the container) through as $1.
sh "$root/scripts/sandbox.sh" sh -c '
set -eu
out="$1"
tmp=$(mktemp -d)
trap "rm -rf \"$tmp\"" EXIT

# --- observed: go vet ./... ---
if go vet ./... >"$tmp/vet.log" 2>&1; then
    if [ -s "$tmp/vet.log" ]; then
        vet="clean ($(grep -c . "$tmp/vet.log") lines of output)"
    else
        vet="clean (0 diagnostics)"
    fi
else
    vet="FAILED"
fi

# --- observed: full suite, counted from the machine-readable stream ---
go test -json ./... >"$tmp/test.json" 2>"$tmp/test.err" || true
passes=$(grep -c "\"Action\":\"pass\".*\"Test\":\"" "$tmp/test.json" || true)
fails=$(grep -c  "\"Action\":\"fail\".*\"Test\":\"" "$tmp/test.json" || true)
skips=$(grep -c  "\"Action\":\"skip\".*\"Test\":\"" "$tmp/test.json" || true)
# package-level results have no "Test" field and do have "Elapsed".
pkgs=$(grep -Ec "\"Action\":\"(pass|fail)\",\"Package\":\"[^\"]*\",\"Elapsed\":" "$tmp/test.json" || true)

# --- observed: cross-compile matrix (pure Go, CGO_ENABLED=0) ---
: >"$tmp/xc"
for pair in linux/arm64 linux/riscv64 linux/ppc64le linux/s390x; do
    os=${pair%/*}; arch=${pair#*/}
    if CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build ./... >/dev/null 2>&1; then
        st=OK
    else
        st=FAIL
    fi
    printf "  %-14s %s\n" "$pair" "$st" >>"$tmp/xc"
done

# --- observed: cgo / unsafe in the root module (the cgo RocksDB bridge is a
#     separate module and is not part of ./...) ---
cgopkgs=$(go list -f "{{if .CgoFiles}}{{.ImportPath}} {{end}}" ./... | tr -d "\n")
unsafepkgs=$(go list -f "{{range .Imports}}{{if eq . \"unsafe\"}}{{$.ImportPath}} {{end}}{{end}}" ./... | tr -d "\n")

deps=$(go list -m all | tail -n +2 | wc -l | tr -d " ")

{
    printf "EKBLOOM-GO — environment capture (GENERATED)\n\n"
    printf "Produced by scripts/environment.sh, which runs every check below INSIDE\n"
    printf "scripts/sandbox.sh (--memory=2g --memory-swap=2g --cpus=2 --pids-limit=512).\n"
    printf "Do not edit by hand; regenerate with:  sh scripts/environment.sh\n"
    printf "CI refreshes it in the \"environment capture (generated)\" step of .github/workflows/ci.yml.\n\n"
    printf "Generated (UTC):      %s\n" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf "Container go:         %s\n" "$(go version)"
    printf "GOOS/GOARCH:          %s/%s\n" "$(go env GOOS)" "$(go env GOARCH)"
    printf "GOMAXPROCS:           %s (pinned to the CPU cap by scripts/sandbox.sh; Go does not read the cgroup quota)\n" "${GOMAXPROCS:-$(nproc)}"
    printf "Host nproc (visible): %s (quota-capped, not cpuset-pinned, so this is the host count)\n" "$(nproc)"
    printf "Kernel:               %s\n" "$(cat /proc/sys/kernel/osrelease)"
    printf "Sandbox caps:         memory=%s cpus=%s pids=%s (memory-swap=%s, nofile=4096)\n" "$EKB_LIMIT_MEM" "$EKB_LIMIT_CPUS" "$EKB_LIMIT_PIDS" "$EKB_LIMIT_MEM"
    printf "Container image:      %s (%s)\n\n" "$EKB_CONTAINER_IMAGE" "$EKB_CONTAINER_IMAGE_ID"
    printf "Module:               %s\n" "$(go list -m)"
    if [ "$deps" = 0 ]; then
        printf "Dependencies:         none (module has no require lines; stdlib only)\n"
    else
        printf "Dependencies:         %s\n" "$deps"
    fi
    printf "cgo packages:         %s\n" "${cgopkgs:-none}"
    printf "unsafe imports:       %s\n\n" "${unsafepkgs:-none}"
    printf "Cross-compilation (pure Go; CGO_ENABLED=0):\n"
    cat "$tmp/xc"
    printf "\nObserved for this commit (inside the sandbox):\n"
    printf "  go vet ./...          %s\n" "$vet"
    printf "  go test ./...         %s passed, %s failed, %s skipped across %s packages with tests\n\n" "$passes" "$fails" "$skips" "$pkgs"
    printf "Reproduce:\n"
    printf "  sh scripts/sandbox.sh go test ./...\n"
    printf "  sh scripts/sandbox.sh go vet ./...\n"
    printf "  sh scripts/sandbox.sh go test -race ./...\n"
} >"$out"

echo "wrote $out"
' sh "$out"

echo
echo "== evidence/environment.txt =="
cat "$out"
