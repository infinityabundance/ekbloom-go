#!/bin/sh
# CPU profiles for the hot paths (m1/m2/m9 encode and the accelerated read).
#
# Emits, under evidence/profile/:
#   <name>.prof    raw CPU profile (pprof protobuf)
#   <name>.svg     go tool pprof call graph (SVG)
#   <name>.top     top functions by flat CPU
#
# If `flamegraph.pl` (Brendan Gregg's FlameGraph) is on PATH, the SVG is a true
# flame graph instead; otherwise it is the pprof call graph. Either way, for the
# same profile, `go tool pprof -http=:0 evidence/profile/<name>.prof` opens the
# interactive flame view in a browser.
#
# Run from the repository root: sh scripts/profile.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
out="$root/evidence/profile"
mkdir -p "$out"
bin="$out/ekbloom.test"
go test -c -o "$bin" ./internal/ekb

have_flamegraph=0
command -v flamegraph.pl >/dev/null 2>&1 && have_flamegraph=1

profile() {
    name=$1
    bench=$2
    prof="$out/$name.prof"
    "$bin" -test.run '^$' -test.bench "$bench" -test.benchtime 300ms -test.cpuprofile "$prof" >/dev/null 2>&1 || true
    go tool pprof -top "$bin" "$prof" > "$out/$name.top" 2>/dev/null || true
    if [ "$have_flamegraph" = 1 ]; then
        go tool pprof -raw "$bin" "$prof" 2>/dev/null | awk '
            /^samples\/count/ { next }
            /^ / { n=0; for (i=2;i<=NF;i++) { f=$i; if (f=="") continue; printf "%s;", f } ; printf " 1\n" }
        ' > "$out/$name.folded" 2>/dev/null || true
        if [ -s "$out/$name.folded" ]; then
            flamegraph.pl --title "$name" "$out/$name.folded" > "$out/$name.svg" 2>/dev/null || true
        fi
    fi
    if [ ! -s "$out/$name.svg" ]; then
        go tool pprof -svg -output "$out/$name.svg" "$bin" "$prof" >/dev/null 2>&1 || true
    fi
    echo "wrote evidence/profile/$name.{prof,svg,top}"
}

profile m1-encode 'BenchmarkMachineEncode/m1'
profile m2-encode 'BenchmarkMachineEncode/m2'
profile m9-encode 'BenchmarkMachineEncode/m9'
profile byteat-pathbloom 'BenchmarkByteAtPathBloom'

rm -f "$bin"
echo
echo "top functions (m1 encode):"
head -8 "$out/m1-encode.top"
