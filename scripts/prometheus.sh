#!/bin/sh
# Build + run the EKBLOOM Prometheus exporter inside the OOM-protected sandbox,
# capture ONE deterministic scrape to evidence/prometheus-scrape.txt, and copy the
# Grafana dashboard to evidence/grafana-dashboard.json.
#
# The exporter lives in integrations/prometheus — a separate Go module, so the
# Prometheus client dependency never enters the core module. Everything heavy runs
# hard-capped (memory + swap, CPUs, pids, fds); nothing runs on the host.
#
# Outputs:
#   evidence/prometheus-scrape.txt  — one real scrape of the exporter's /metrics
#   evidence/prometheus-scrape.log  — build/eval log + the exporter's stderr
#   evidence/grafana-dashboard.json — verbatim copy of grafana/ekbloom-dashboard.json
#
# Run from anywhere: sh scripts/prometheus.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

image=${PROMETHEUS_IMAGE:-ekbloom-go-sandbox:latest}
mem=${PROMETHEUS_MEM:-2g}
cpus=${PROMETHEUS_CPUS:-2}
out="$root/evidence/prometheus-scrape.txt"
log="$root/evidence/prometheus-scrape.log"
mkdir -p "$root/evidence"

# Reuse the existing sandbox image (it is the capped Go runner); build it if absent.
if ! docker image inspect "$image" >/dev/null 2>&1; then
    echo "== building sandbox image ($image) ==" >&2
    docker build -f Dockerfile.sandbox -t "$image" . >&2
fi

# --- run (hard-capped); stdout is the scrape artifact, stderr is the log -------
echo "== docker run --memory=$mem --memory-swap=$mem --cpus=$cpus $image ==" >"$log"
echo "== go -C integrations/prometheus mod tidy && go -C integrations/prometheus run ./cmd/ekbloom-exporter --scrape ==" >>"$log"
if ! docker run --rm \
      --memory="$mem" --memory-swap="$mem" \
      --cpus="$cpus" \
      --pids-limit=512 \
      --ulimit nofile=4096:4096 \
      --ulimit nproc=1024:1024 \
      -e GOFLAGS="-mod=mod -buildvcs=false" \
      -v "$root":/src -w /src \
      -v ekbloom-gocache:/tmp/gocache \
      -v ekbloom-gomod:/tmp/gomod \
      "$image" \
      sh -c 'go -C integrations/prometheus mod tidy && go -C integrations/prometheus run ./cmd/ekbloom-exporter --scrape' \
      >"$out" 2>>"$log"; then
    echo "prometheus exporter failed; log follows:" >&2
    cat "$log" >&2
    exit 1
fi

# --- the dashboard ships with the evidence next to the scrape ------------------
cp "$root/grafana/ekbloom-dashboard.json" "$root/evidence/grafana-dashboard.json"

cat "$log" >&2
echo
echo "== evidence/prometheus-scrape.txt =="
cat "$out"
