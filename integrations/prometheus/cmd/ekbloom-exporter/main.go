// Command ekbloom-exporter exposes a running EKBLOOM object as Prometheus
// metrics on :9100/metrics.
//
// It builds (or loads with --fixture/--dict) a representative machine-9 `.ekb`
// object, registers the five EKBLOOM metric families on a private registry, runs
// a short loop of Gets so the ekbloom_get_ns histogram has samples, and serves
// /metrics.
//
// Modes:
//
//	ekbloom-exporter                 # serve :9100 until SIGINT/SIGTERM
//	ekbloom-exporter --scrape        # run one workload pass, scrape /metrics
//	                                 # once, print it to stdout, exit
//	ekbloom-exporter --fixture f.ekb --dict f.dict
//
// The --scrape mode is deterministic and long-running-free, so CI can capture a
// real scrape to evidence/prometheus-scrape.txt (see scripts/prometheus.sh).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	ekbloom "github.com/infinityabundance/ekbloom-go"
	"github.com/infinityabundance/ekbloom-go/integrations/prometheus/exporter"
)

func main() {
	var (
		addr    = flag.String("addr", ":9100", "HTTP listen address for /metrics")
		scrape  = flag.Bool("scrape", false, "run one workload pass, scrape /metrics once, print it to stdout and exit")
		gets    = flag.Int("gets", 2000, "reads per workload pass (histogram samples)")
		records = flag.Int("records", 512, "corpus records for the synthesized object")
		recLen  = flag.Int("record-bytes", 100, "logical bytes per corpus record")
		train   = flag.Int("train-records", 128, "records used to train the shared dictionary")
		readLen = flag.Int("read-len", 4096, "ReadAt length in bytes")
		fixture = flag.String("fixture", "", "path to a prebuilt .ekb to load (requires --dict)")
		dict    = flag.String("dict", "", "path to the dictionary for --fixture")
	)
	flag.Parse()

	cfg := exporter.DefaultConfig()
	cfg.Gets = *gets
	cfg.Records = *records
	cfg.RecordBytes = *recLen
	cfg.TrainRecords = *train
	cfg.ReadLen = *readLen
	cfg.FixturePath = *fixture
	cfg.DictPath = *dict

	exp, err := exporter.New(cfg)
	if err != nil {
		fail(err)
	}
	if err := exp.RunWorkload(); err != nil {
		fail(err)
	}

	m := exp.ObjectMetrics()
	fmt.Fprintf(os.Stderr, "ekbloom-exporter: logical=%d visible=%d physical=%d E_V=%.6f breaches=%d\n",
		m.LogicalLen, m.VisibleBytes, m.PhysicalBytes, m.ExpansionRatio, ekbloom.Breaches())

	if *scrape {
		body, err := exp.ScrapeOnce(*addr)
		if err != nil {
			fail(err)
		}
		// stdout is purely the Prometheus exposition, so the caller can capture
		// it verbatim.
		if _, err := os.Stdout.Write(body); err != nil {
			fail(err)
		}
		return
	}

	srv, ln, err := exp.Serve(*addr)
	if err != nil {
		fail(err)
	}
	fmt.Fprintf(os.Stderr, "ekbloom-exporter listening on http://%s/metrics\n", ln.Addr())

	// Keep the gauges and histogram fresh while the server runs.
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if err := exp.RunWorkload(); err != nil {
					fmt.Fprintln(os.Stderr, "workload:", err)
				}
			}
		}
	}()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	<-ctx.Done()
	close(stop)
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	_ = srv.Shutdown(shutCtx)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "ekbloom-exporter:", err)
	os.Exit(1)
}
