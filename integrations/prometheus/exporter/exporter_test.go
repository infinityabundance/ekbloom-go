package exporter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
)

// expectedFamilies are the exact metric families the exporter must expose.
var expectedFamilies = []string{
	MetricPhysicalBytes,
	MetricExpansionRatio,
	MetricBreachTotal,
	MetricGetNS,
	MetricWorstCaseStepsFor,
}

// gather runs a workload pass and returns the gathered families by name.
func gather(t *testing.T) map[string]*dto.MetricFamily {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Gets = 64
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if e.LogicalLen() == 0 {
		t.Fatal("object has zero logical length")
	}
	if err := e.RunWorkload(); err != nil {
		t.Fatalf("RunWorkload: %v", err)
	}
	mfs, err := e.Registry().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	byName := make(map[string]*dto.MetricFamily, len(mfs))
	for _, mf := range mfs {
		byName[mf.GetName()] = mf
	}
	return byName
}

func TestExporterExposesExactlyFiveFamilies(t *testing.T) {
	byName := gather(t)

	if len(byName) != len(expectedFamilies) {
		t.Errorf("gathered %d families, want exactly %d: %v", len(byName), len(expectedFamilies), familyNames(byName))
	}
	for _, name := range expectedFamilies {
		if _, ok := byName[name]; !ok {
			t.Errorf("missing metric family %q", name)
		}
	}
}

func TestMetricTypes(t *testing.T) {
	byName := gather(t)

	want := map[string]dto.MetricType{
		MetricPhysicalBytes:     dto.MetricType_GAUGE,
		MetricExpansionRatio:    dto.MetricType_GAUGE,
		MetricBreachTotal:       dto.MetricType_COUNTER,
		MetricGetNS:             dto.MetricType_HISTOGRAM,
		MetricWorstCaseStepsFor: dto.MetricType_GAUGE,
	}
	for name, typ := range want {
		mf, ok := byName[name]
		if !ok {
			t.Errorf("missing %q", name)
			continue
		}
		if mf.GetType() != typ {
			t.Errorf("%s: type %v, want %v", name, mf.GetType(), typ)
		}
	}
}

func TestHistogramHasSamplesAndWorstCaseIsPositive(t *testing.T) {
	byName := gather(t)

	var count uint64
	for _, m := range byName[MetricGetNS].GetMetric() {
		count += m.GetHistogram().GetSampleCount()
	}
	if count == 0 {
		t.Error("ekbloom_get_ns has no samples")
	}

	physical := byName[MetricPhysicalBytes].GetMetric()[0].GetGauge().GetValue()
	if physical <= 0 {
		t.Errorf("ekbloom_physical_bytes = %v, want > 0", physical)
	}
	expansion := byName[MetricExpansionRatio].GetMetric()[0].GetGauge().GetValue()
	if expansion <= 0 {
		t.Errorf("ekbloom_expansion_ratio = %v, want > 0", expansion)
	}

	steps := byName[MetricWorstCaseStepsFor].GetMetric()
	if len(steps) == 0 {
		t.Fatal("ekbloom_worst_case_steps_for has no series")
	}
	for _, m := range steps {
		if m.GetGauge().GetValue() <= 0 {
			t.Errorf("ekbloom_worst_case_steps_for = %v, want > 0", m.GetGauge().GetValue())
		}
	}
}

// TestScrapeOnce performs a real HTTP scrape of the exporter's own /metrics.
func TestScrapeOnce(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Gets = 32
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := e.RunWorkload(); err != nil {
		t.Fatalf("RunWorkload: %v", err)
	}
	body, err := e.ScrapeOnce("127.0.0.1:0")
	if err != nil {
		t.Fatalf("ScrapeOnce: %v", err)
	}
	text := string(body)
	for _, name := range expectedFamilies {
		if !strings.Contains(text, name) {
			t.Errorf("scrape output missing %q", name)
		}
	}
}

// TestDashboardValidJSON proves grafana/ekbloom-dashboard.json parses and that
// every panel target references one of the exporter's metric names.
func TestDashboardValidJSON(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
	path := filepath.Join(root, "grafana", "ekbloom-dashboard.json")

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read dashboard: %v", err)
	}
	if !json.Valid(raw) {
		t.Fatal("dashboard is not valid JSON")
	}

	var dash struct {
		Panels []struct {
			Title   string `json:"title"`
			Targets []struct {
				Expr string `json:"expr"`
			} `json:"targets"`
		} `json:"panels"`
	}
	if err := json.Unmarshal(raw, &dash); err != nil {
		t.Fatalf("unmarshal dashboard: %v", err)
	}
	if len(dash.Panels) != 5 {
		t.Fatalf("dashboard has %d panels, want 5", len(dash.Panels))
	}

	joined := ""
	for _, p := range dash.Panels {
		for _, tgt := range p.Targets {
			joined += tgt.Expr + "\n"
		}
	}
	for _, want := range []string{
		MetricPhysicalBytes,
		MetricExpansionRatio,
		"rate(" + MetricBreachTotal + "[5m])",
		"histogram_quantile(0.5",
		"histogram_quantile(0.99",
		MetricWorstCaseStepsFor,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("no dashboard panel references %q", want)
		}
	}
}

func familyNames(m map[string]*dto.MetricFamily) []string {
	out := make([]string, 0, len(m))
	for name := range m {
		out = append(out, name)
	}
	return out
}
