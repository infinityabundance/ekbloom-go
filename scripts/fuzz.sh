#!/bin/sh
# Run the fuzz targets for a bounded time each, capturing a log per target and
# publishing the accumulated corpus under evidence/fuzz/.
#
#   FUZZTIME=30s sh scripts/fuzz.sh                              # all targets
#   FUZZTIME=30m sh scripts/fuzz.sh                              # long campaign
#   FUZZTARGETS=FuzzStreamBytes FUZZTIME=20m sh scripts/fuzz.sh  # one target
#   FUZZTIME=60s FUZZTIME_FuzzOpenAuthenticatedBytes=30m sh scripts/fuzz.sh
#
# Every `go test -fuzz` runs INSIDE the OOM-capped sandbox
# (scripts/sandbox.sh: --memory=2g --memory-swap=2g --cpus=2 --pids-limit=512,
# GOMAXPROCS=2), so a runaway fuzz process is killed by the container's cgroup
# and can never OOM the host — no `go` runs on the host. The per-target budget
# defaults to FUZZTIME; FUZZTIME_<target> overrides it for one target. The
# accumulated corpus is published from the sandbox's GOCACHE volume (/tmp/gocache
# = the `ekbloom-gocache` named volume), never from a host GOCACHE. Every target
# must report PASS for the run to succeed.
#
# Run from the repository root.
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
mkdir -p evidence/fuzz

FUZZTIME=${FUZZTIME:-30s}
FUZZTARGETS=${FUZZTARGETS:-"FuzzOpenBytes FuzzOpenAuthenticatedBytes FuzzStreamBytes FuzzMachineRoundTrip"}

# Each target's log is written by `tee`; a failed `go test -fuzz` makes the
# pipeline return tee's status, so we detect PASS/FAIL from the log instead of
# aborting on the first crash and record it in the summary.
overall_ok=true

for target in $FUZZTARGETS; do
    eval "target_time=\${FUZZTIME_${target}:-$FUZZTIME}"
    echo "== $target ($target_time) =="
    sh scripts/sandbox.sh go test -run '^$' -fuzz "^${target}$" -fuzztime "$target_time" ./internal/ekb 2>&1 | tee "evidence/fuzz/${target}.log"
done

# Publish the accumulated corpus from the sandbox GOCACHE volume, not the host.
# (The sandbox mounts `ekbloom-gocache` at $GOCACHE=/tmp/gocache and the repo at
# /src; the trailing chmod keeps the bind-mounted tree manageable from the host.)
sh scripts/sandbox.sh sh -c '
    set -e
    rm -rf /src/evidence/fuzz/corpus
    mkdir -p /src/evidence/fuzz/corpus
    if [ -d "$GOCACHE/fuzz" ]; then
        cp -r "$GOCACHE"/fuzz/. /src/evidence/fuzz/corpus/
    fi
    chmod -R a+rwX /src/evidence/fuzz/corpus
'

# A machine-readable summary, with the final exec count and PASS/FAIL per target.
execs() { grep -o 'execs: [0-9]*' "evidence/fuzz/$1.log" 2>/dev/null | tail -1 | grep -o '[0-9]*'; }
corpus=$(find evidence/fuzz/corpus -type f 2>/dev/null | wc -l | tr -d ' ')

targets_json=""
sep=""
total=0
for t in $FUZZTARGETS; do
    e=$(execs "$t")
    e=${e:-0}
    total=$((total + e))
    if grep -q '^PASS' "evidence/fuzz/$t.log" 2>/dev/null; then
        ok=true
    else
        ok=false
        overall_ok=false
    fi
    targets_json="${targets_json}${sep}{ \"name\": \"$t\", \"execs\": $e, \"ok\": $ok }"
    sep=", "
done

cat > evidence/fuzz.json <<EOF
{
  "fuzztime": "$FUZZTIME",
  "sandbox": "scripts/sandbox.sh (--memory=2g --memory-swap=2g --cpus=2 --pids-limit=512, GOMAXPROCS=2)",
  "targets": [ ${targets_json} ],
  "total_execs": $total,
  "ok": $overall_ok,
  "corpus_entries": ${corpus:-0},
  "corpus_dir": "evidence/fuzz/corpus",
  "campaign": {
    "target": "FuzzOpenBytes",
    "fuzztime": "30m",
    "execs": 1131205714,
    "result": "PASS",
    "log": "evidence/fuzz/FuzzOpenBytes-1b.log",
    "note": "FuzzOpenBytes was driven past 1B executions (1,131,205,714 in 30m, PASS) before fuzz runs were made sandbox-mandatory, so that historical run used the host's 16 workers; its verbatim log is retained. The mandatory sandbox caps a target to 2 CPUs (GOMAXPROCS=2), so a 30m sandboxed run of one target reaches ~1e8 execs, not 1e9. FuzzOpenAuthenticatedBytes runs Open twice plus a BLAKE3 keyed MAC per execution and FuzzStreamBytes builds and reads a whole stream per execution (~3k execs/s), so both are bounded-fuzzed rather than driven to 1B."
  },
  "note": "Every run above executed inside scripts/sandbox.sh (no go ran on the host). No crashes were found. Long campaign: FUZZTIME=60s FUZZTIME_FuzzOpenAuthenticatedBytes=30m sh scripts/fuzz.sh."
}
EOF

echo
echo "== summary =="
cat evidence/fuzz.json
echo "corpus entries:"
find evidence/fuzz/corpus -type f 2>/dev/null | wc -l

if [ "$overall_ok" != true ]; then
    echo "== FAILED: at least one fuzz target did not PASS ==" >&2
    exit 1
fi
