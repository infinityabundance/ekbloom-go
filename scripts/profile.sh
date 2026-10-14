#!/bin/sh
# CPU + heap profiles and GC-pause measurements for the hot paths (m1/m2/m9 encode
# and the accelerated read), all produced inside the OOM-capped sandbox.
#
# Nothing heavy runs on the host: the test-binary build, the benchmark runs and
# every `go tool pprof` invocation go through scripts/sandbox.sh
# (--memory=2g --memory-swap=2g --cpus=2 --pids-limit=512). `go tool pprof -svg`
# needs Graphviz `dot`, which the sandbox image provides (Dockerfile.sandbox).
#
# Emits, under evidence/profile/ (per hot path <name>):
#   <name>.prof        raw CPU profile (pprof protobuf)
#   <name>.mem.prof    raw heap profile (-test.memprofile)
#   <name>.top         top functions by flat CPU
#   <name>.alloc.top   allocation breakdown   (pprof -top -alloc_space)
#   <name>.inuse.top   retained heap          (pprof -top -inuse_space)
#   <name>.svg         CPU call graph (SVG); `pprof -http=:0 <name>.prof` is the
#                      interactive flame view
#   <name>.heap.svg    heap call graph (SVG)
# plus the GC-pause / heap measurement and the consolidated report:
#   gcstats.json       GC pause quantiles + peak/retained heap
#   ../profile.json    per hot path alloc sites, flat CPU shares, retained bytes,
#                      and the GC-pause count/total/quantiles + peak heap
#
# Run from the repository root: sh scripts/profile.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
out="$root/evidence/profile"
mkdir -p "$out"

# Every heavy step runs in the sandbox via the shared docker wrapper.
sandbox() { sh "$root/scripts/sandbox.sh" "$@"; }

bin_rel="evidence/profile/ekbloom.test"
bin="$root/$bin_rel"

echo "== building the test binary (sandbox) =="
sandbox go test -c -o "$bin_rel" ./internal/ekb

# profile <name> <benchmark>: capture CPU + heap, then render every artifact.
profile() {
    name=$1
    bench=$2
    sandbox "./$bin_rel" \
        -test.run '^$' -test.bench "$bench" -test.benchtime 300ms \
        -test.cpuprofile "evidence/profile/$name.prof" \
        -test.memprofile "evidence/profile/$name.mem.prof" \
        -test.memprofilerate 4096 >/dev/null 2>&1 || true

    sandbox go tool pprof -top "./$bin_rel" "evidence/profile/$name.prof" \
        > "$out/$name.top" 2>/dev/null || true
    sandbox go tool pprof -top -alloc_space "./$bin_rel" "evidence/profile/$name.mem.prof" \
        > "$out/$name.alloc.top" 2>/dev/null || true
    sandbox go tool pprof -top -inuse_space "./$bin_rel" "evidence/profile/$name.mem.prof" \
        > "$out/$name.inuse.top" 2>/dev/null || true

    sandbox go tool pprof -svg "./$bin_rel" "evidence/profile/$name.prof" \
        > "$out/$name.svg" 2>/dev/null || true
    sandbox go tool pprof -svg -inuse_space "./$bin_rel" "evidence/profile/$name.mem.prof" \
        > "$out/$name.heap.svg" 2>/dev/null || true

    echo "wrote evidence/profile/$name.{prof,mem.prof,top,alloc.top,inuse.top,svg,heap.svg}"
}

profile m1-encode 'BenchmarkMachineEncode/m1'
profile m2-encode 'BenchmarkMachineEncode/m2'
profile m9-encode 'BenchmarkMachineEncode/m9'
profile byteat-pathbloom 'BenchmarkByteAtPathBloom'

echo "== GC pauses + heap, and the consolidated report (sandbox) =="
sandbox go run ./internal/tools/profstats \
    --dir evidence/profile \
    --out evidence/profile.json \
    --path m1-encode='BenchmarkMachineEncode/m1' \
    --path m2-encode='BenchmarkMachineEncode/m2' \
    --path m9-encode='BenchmarkMachineEncode/m9' \
    --path byteat-pathbloom='BenchmarkByteAtPathBloom'

rm -f "$bin"
echo
echo "top allocation sites (m1 encode):"
head -12 "$out/m1-encode.alloc.top"
echo
echo "artifacts: evidence/profile/ + evidence/profile.json"
