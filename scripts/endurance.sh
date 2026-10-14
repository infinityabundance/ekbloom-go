#!/bin/sh
# Long-run writer endurance / fault-injection harness, run hard-capped inside the
# sandbox.
#
# Drives TestEndurance: many rounds of "append a generation, damage it (truncate /
# bit-flip / older-generation damage / header-core truncation), assert Open returns
# exactly the newest surviving valid generation or a typed error, repair with
# TruncateToValid, continue" plus a concurrent-reader phase. Nothing runs on the
# host: the harness is compiled and executed inside the OOM-protected container
# provided by scripts/sandbox.sh (--memory=2g --memory-swap=2g --cpus=2
# --pids-limit=512, GOMAXPROCS=2).
#
# Tunables (forwarded into the container; all optional):
#   ENDURANCE_ROUNDS  fault-injection rounds   (default 2000; -short runs 200)
#   ENDURANCE_SEED    deterministic PRNG seed  (default 1)
#
# Outputs:
#   evidence/endurance.json  per-kind counts, recovered/typed/garbage, peak heap
#   evidence/endurance.log   the human-readable fault-injection log
#
# Run from the repo root:
#   sh scripts/endurance.sh
# A longer endurance run:
#   ENDURANCE_ROUNDS=200000 sh scripts/endurance.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
mkdir -p "$root/evidence"

rounds=${ENDURANCE_ROUNDS:-2000}
seed=${ENDURANCE_SEED:-1}

# sandbox.sh execs a hard-capped `docker run`; `env` injects the endurance
# tunables into the container without threading them through the runner itself.
# The test itself writes both evidence artifacts.
sh scripts/sandbox.sh env \
    EKB_ENDURANCE_ROUNDS="$rounds" \
    EKB_ENDURANCE_SEED="$seed" \
    go test -race -run '^TestEndurance$' -v ./internal/ekb
