// Package exporter exposes a running EKBLOOM object as Prometheus metrics.
//
// It is a thin adapter over the public ekbloom API, not a re-implementation:
//
//   - ekbloom_physical_bytes  and ekbloom_expansion_ratio come straight from
//     Reader.Metrics() (PhysicalBytes, ExpansionRatio = E_V);
//   - ekbloom_breach_total is the process-wide resource-policy counter read live
//     from ekbloom.Breaches() at each scrape (ekbloom.ResourcePolicySnapshot);
//   - ekbloom_get_ns is a histogram that observes the latency of every ByteAt,
//     ReadAt and DecodeInto performed by RunWorkload, so p50/p99 are derivable
//     with histogram_quantile;
//   - ekbloom_worst_case_steps_for is the certified bound returned by
//     Reader.WorstCaseStepsFor.
//
// The metric names are fixed constants because the Grafana dashboard and the CI
// scrape assert on them by name.
package exporter

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	ekbloom "github.com/infinityabundance/ekbloom-go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// The exact metric names this exporter exposes. Keep in sync with
// grafana/ekbloom-dashboard.json and scripts/prometheus.sh.
const (
	MetricPhysicalBytes     = "ekbloom_physical_bytes"
	MetricExpansionRatio    = "ekbloom_expansion_ratio"
	MetricBreachTotal       = "ekbloom_breach_total"
	MetricGetNS             = "ekbloom_get_ns"
	MetricWorstCaseStepsFor = "ekbloom_worst_case_steps_for"
)

// Config selects the representative object and the read workload.
type Config struct {
	// Corpus geometry for the object synthesized when FixturePath is empty.
	Records      int // number of corpus records
	RecordBytes  int // logical bytes per record
	TrainRecords int // records used to train the shared dictionary

	// FixturePath and DictPath, when both set, load a prebuilt `.ekb` image and
	// its dictionary instead of synthesizing one from a corpus.
	FixturePath string
	DictPath    string

	// Gets is the number of sampled reads performed per RunWorkload pass.
	Gets int
	// ReadLen is the ReadAt length in bytes (ByteAt reads a single byte).
	ReadLen int
}

// DefaultConfig is a small, fast, deterministic workload.
func DefaultConfig() Config {
	return Config{
		Records:      512,
		RecordBytes:  100,
		TrainRecords: 128,
		Gets:         2000,
		ReadLen:      4096,
	}
}

func (c Config) withDefaults() Config {
	d := DefaultConfig()
	if c.Records <= 0 {
		c.Records = d.Records
	}
	if c.RecordBytes <= 0 {
		c.RecordBytes = d.RecordBytes
	}
	if c.TrainRecords <= 0 || c.TrainRecords > c.Records {
		c.TrainRecords = min(d.TrainRecords, c.Records)
	}
	if c.Gets <= 0 {
		c.Gets = d.Gets
	}
	if c.ReadLen <= 0 {
		c.ReadLen = d.ReadLen
	}
	return c
}

// Exporter owns the opened object, its metrics and an optional HTTP server.
type Exporter struct {
	cfg    Config
	reg    *prometheus.Registry
	dict   ekbloom.Dictionary
	source []byte
	image  []byte
	reader *ekbloom.Reader
	dec    *ekbloom.DictDecoder

	// Per-record machine-9 cores used to exercise the DecodeInto path.
	cores   [][]byte
	dst     []byte
	logical uint64

	physical  prometheus.Gauge
	expansion prometheus.Gauge
	getNS     *prometheus.HistogramVec
	worstCase *prometheus.GaugeVec
	breach    *breachCollector
}

// New builds (or loads) the representative object, opens it with the real API,
// and registers the five metric families on a private registry.
func New(cfg Config) (*Exporter, error) {
	cfg = cfg.withDefaults()
	obj, err := buildObject(cfg)
	if err != nil {
		return nil, err
	}

	img, err := ekbloom.WriteBodyWithDict(obj.source, ekbloom.Profile{Kind: ekbloom.ProfileRoot}, obj.dict)
	if err != nil {
		return nil, fmt.Errorf("WriteBodyWithDict: %w", err)
	}
	r, err := ekbloom.OpenWithDict(img, obj.dict)
	if err != nil {
		return nil, fmt.Errorf("OpenWithDict: %w", err)
	}
	dec, err := ekbloom.NewDictDecoder(obj.dict)
	if err != nil {
		return nil, fmt.Errorf("NewDictDecoder: %w", err)
	}

	cores := make([][]byte, len(obj.records))
	maxLen := 0
	for i, rec := range obj.records {
		core, err := obj.dict.EncodeRecord(rec)
		if err != nil {
			return nil, fmt.Errorf("EncodeRecord: %w", err)
		}
		cores[i] = core
		if len(rec) > maxLen {
			maxLen = len(rec)
		}
	}

	e := &Exporter{
		cfg:     cfg,
		reg:     prometheus.NewRegistry(),
		dict:    obj.dict,
		source:  obj.source,
		image:   img,
		reader:  r,
		dec:     dec,
		cores:   cores,
		dst:     make([]byte, maxLen),
		logical: uint64(len(obj.source)),
	}

	e.physical = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: MetricPhysicalBytes,
		Help: "Manifested physical bytes of the opened EKBLOOM object: core plus every structure.",
	})
	e.expansion = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: MetricExpansionRatio,
		Help: "Concealed expansion ratio E_V = physical_bytes / visible_bytes of the opened object.",
	})
	e.getNS = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    MetricGetNS,
		Help:    "EKBLOOM read latency in nanoseconds, observed for every ByteAt/ReadAt/DecodeInto.",
		Buckets: []float64{100, 250, 500, 1000, 2000, 5000, 10000, 25000, 50000, 100000, 250000, 500000, 1000000},
	}, []string{"op"})
	e.worstCase = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: MetricWorstCaseStepsFor,
		Help: "Certified upper bound on machine positioning steps plus output reveals from Reader.WorstCaseStepsFor(0, range).",
	}, []string{"range"})
	e.breach = newBreachCollector()

	e.reg.MustRegister(e.physical, e.expansion, e.getNS, e.worstCase, e.breach)
	return e, nil
}

// Registry returns the private registry (only the five EKBLOOM families).
func (e *Exporter) Registry() *prometheus.Registry { return e.reg }

// LogicalLen is the object's logical byte length.
func (e *Exporter) LogicalLen() uint64 { return e.logical }

// ImageLen is the serialized `.ekb` size.
func (e *Exporter) ImageLen() int { return len(e.image) }

// ObjectMetrics returns the live per-object accounting snapshot.
func (e *Exporter) ObjectMetrics() ekbloom.ReaderMetrics { return e.reader.Metrics() }

// VisibleBytes is the visible core length V.
func (e *Exporter) VisibleBytes() uint64 { return e.reader.Metrics().VisibleBytes }

// RunWorkload refreshes the object gauges, the certified worst-case bound, and
// then issues Gets so the latency histogram has fresh samples. It is safe to call
// repeatedly.
func (e *Exporter) RunWorkload() error {
	m := e.reader.Metrics()
	e.physical.Set(float64(m.PhysicalBytes))
	e.expansion.Set(m.ExpansionRatio)

	if err := e.refreshWorstCase(); err != nil {
		return err
	}
	return e.runReads()
}

// ranges are the request sizes the worst-case bound is reported for.
func (e *Exporter) ranges() []uint64 {
	candidates := []uint64{1, 64, 1024, 4096, 65536}
	out := make([]uint64, 0, len(candidates))
	for _, n := range candidates {
		if n <= e.logical {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		out = append(out, 1)
	}
	return out
}

func (e *Exporter) refreshWorstCase() error {
	for _, n := range e.ranges() {
		steps, err := e.reader.WorstCaseStepsFor(0, n)
		if err != nil {
			return fmt.Errorf("WorstCaseStepsFor(0, %d): %w", n, err)
		}
		e.worstCase.WithLabelValues(fmt.Sprintf("%d", n)).Set(float64(steps))
	}
	return nil
}

func (e *Exporter) runReads() error {
	readLen := e.cfg.ReadLen
	if readLen <= 0 || uint64(readLen) > e.logical {
		readLen = int(e.logical)
	}
	if readLen < 1 {
		readLen = 1
	}

	s := uint64(0x5EED1234)
	for i := 0; i < e.cfg.Gets; i++ {
		off := nextOff(&s, e.logical)
		t0 := time.Now()
		if _, err := e.reader.ByteAt(off); err != nil {
			return fmt.Errorf("ByteAt(%d): %w", off, err)
		}
		e.getNS.WithLabelValues("byte_at").Observe(float64(time.Since(t0).Nanoseconds()))

		roff := nextOff(&s, e.logical-uint64(readLen)+1)
		t0 = time.Now()
		if _, err := e.reader.ReadAt(roff, uint64(readLen)); err != nil {
			return fmt.Errorf("ReadAt(%d, %d): %w", roff, readLen, err)
		}
		e.getNS.WithLabelValues("read_at").Observe(float64(time.Since(t0).Nanoseconds()))

		if len(e.cores) > 0 {
			core := e.cores[i%len(e.cores)]
			t0 = time.Now()
			if _, err := e.dec.DecodeInto(e.dst, core); err != nil {
				return fmt.Errorf("DecodeInto: %w", err)
			}
			e.getNS.WithLabelValues("decode_into").Observe(float64(time.Since(t0).Nanoseconds()))
		}
	}
	return nil
}

// nextOff advances a deterministic LCG and returns an offset in [0, n).
func nextOff(s *uint64, n uint64) uint64 {
	if n == 0 {
		return 0
	}
	*s = *s*6364136223846793005 + 1442695040888963407
	return (*s >> 20) % n
}

// Handler returns the /metrics handler for the private registry.
func (e *Exporter) Handler() http.Handler {
	return promhttp.HandlerFor(e.reg, promhttp.HandlerOpts{EnableOpenMetrics: false})
}

// Serve starts an HTTP server on addr serving /metrics and returns it with its
// listener. The caller is responsible for Shutdown.
func (e *Exporter) Serve(addr string) (*http.Server, net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", e.Handler())
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return srv, ln, nil
}

// ScrapeOnce starts the server on addr, performs exactly one HTTP GET of its own
// /metrics, returns the exposition body, and shuts the server down. It exists so
// CI can capture a deterministic scrape without a long-running server.
func (e *Exporter) ScrapeOnce(addr string) ([]byte, error) {
	srv, ln, err := e.Serve(addr)
	if err != nil {
		return nil, err
	}
	defer func() { _ = srv.Shutdown(context.Background()) }()

	host, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		return nil, err
	}
	if host == "" || host == "::" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	url := "http://" + net.JoinHostPort(host, port) + "/metrics"

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("scrape %s: unexpected status %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// breachCollector reports the process-wide allocation-breach counter as a
// Prometheus counter, reading ekbloom.Breaches() live at each scrape.
type breachCollector struct {
	desc *prometheus.Desc
}

func newBreachCollector() *breachCollector {
	return &breachCollector{desc: prometheus.NewDesc(
		MetricBreachTotal,
		"Total whole-object allocations the EKBLOOM resource policy has rejected (ekbloom.Breaches()).",
		nil, nil,
	)}
}

func (c *breachCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c *breachCollector) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.MustNewConstMetric(c.desc, prometheus.CounterValue, float64(ekbloom.Breaches()))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
