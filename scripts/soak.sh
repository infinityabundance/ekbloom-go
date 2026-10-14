#!/bin/sh
# Randomized round-trip soak harness, run hard-capped inside the sandbox.
#
# Repeatedly round-trips randomized objects through the Go implementation while
# accumulating a cumulative logical-symbol counter, proving that memory stays
# bounded well under the sandbox's 2 GiB cap. Nothing runs on the host: the
# harness is compiled and executed inside the OOM-protected container provided
# by scripts/sandbox.sh (--memory=2g --memory-swap=2g --cpus=2 --pids-limit=512).
#
# Tunables (forwarded into the container; all optional):
#   SOAK_SYMBOLS  logical-symbol target       (default 1000000000)
#   SOAK_SECONDS  wall-clock budget, seconds   (default 120)
#   SOAK_SEED     deterministic PRNG seed      (default 1)
#
# Outputs:
#   evidence/soak.json  the final JSON report (program stdout)
#   evidence/soak.log   progress + diagnostics (program stderr)
#
# Run from the repo root:
#   sh scripts/soak.sh
# A long soak (default: stop after 1e9 symbols or 24h, whichever first):
#   SOAK_SECONDS=86400 sh scripts/soak.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
mkdir -p "$root/evidence"

symbols=${SOAK_SYMBOLS:-1000000000}
seconds=${SOAK_SECONDS:-120}
seed=${SOAK_SEED:-1}

# sandbox.sh execs a hard-capped `docker run`; `env` injects the soak tunables
# into the container without threading them through the runner itself.
sh scripts/sandbox.sh env \
    SOAK_SYMBOLS="$symbols" \
    SOAK_SECONDS="$seconds" \
    SOAK_SEED="$seed" \
    go run ./internal/tools/soak \
    >"$root/evidence/soak.json" 2>"$root/evidence/soak.log"
