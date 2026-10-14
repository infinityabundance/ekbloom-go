package ekbloom

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestReaderMetricsAndExport proves the per-object metrics snapshot and the
// resource-policy exports (Prometheus-style text and JSON) are consistent with
// the Reader's own accounting.
func TestReaderMetricsAndExport(t *testing.T) {
	src := sampleSource(0, 2048)
	ekb, err := WriteBody(1, src, pathProfile(4))
	if err != nil {
		t.Fatal(err)
	}
	r, err := Open(ekb)
	if err != nil {
		t.Fatal(err)
	}

	m := r.Metrics()
	if m.MachineID != 1 || m.LogicalLen != uint64(len(src)) {
		t.Fatalf("metrics identity: %+v", m)
	}
	if m.PhysicalBytes != r.PhysicalBytes() || m.VisibleBytes != r.VisibleBytes() {
		t.Fatalf("metrics do not match the reader: %+v", m)
	}
	if m.ConcealedBytes != m.PhysicalBytes-m.VisibleBytes {
		t.Fatalf("concealed = %d, want %d", m.ConcealedBytes, m.PhysicalBytes-m.VisibleBytes)
	}
	want := float64(m.PhysicalBytes) / float64(m.VisibleBytes)
	if m.ExpansionRatio != want {
		t.Fatalf("expansion ratio %v, want %v", m.ExpansionRatio, want)
	}

	txt := MetricsText(r)
	for _, needle := range []string{
		"ekbloom_resource_max_single_allocation ",
		"ekbloom_resource_allocation_breaches ",
		"ekbloom_physical_bytes ",
		"ekbloom_visible_bytes ",
		"ekbloom_expansion_ratio ",
		"ekbloom_logical_len ",
		"ekbloom_generation ",
	} {
		if !strings.Contains(txt, needle) {
			t.Fatalf("MetricsText missing %q:\n%s", needle, txt)
		}
	}

	blob, err := MetricsJSON(r)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(blob, &obj); err != nil {
		t.Fatal(err)
	}
	if _, ok := obj["resource"]; !ok {
		t.Fatalf("MetricsJSON missing resource: %s", blob)
	}
	if _, ok := obj["object"]; !ok {
		t.Fatalf("MetricsJSON missing object: %s", blob)
	}
}

// TestResourcePolicyBreachCounter proves the 1 GiB default cap, the refusal of an
// over-cap declared length, and that the breach counter is exported.
func TestResourcePolicyBreachCounter(t *testing.T) {
	SetMaxSingleAllocation(DefaultMaxSingleAllocation)
	defer SetMaxSingleAllocation(DefaultMaxSingleAllocation)
	if got := MaxSingleAllocation(); got != DefaultMaxSingleAllocation {
		t.Fatalf("default cap %d, want %d", got, DefaultMaxSingleAllocation)
	}

	// A declared length over the cap is refused with a typed error and counted.
	ResetBreaches()
	if err := checkDefaultAllocation(DefaultMaxSingleAllocation + 1); err == nil {
		t.Fatal("over-cap allocation accepted")
	} else {
		var e *Error
		if !errors.As(err, &e) || e.Kind != KindAllocationTooLarge {
			t.Fatalf("wrong error: %v", err)
		}
	}
	if Breaches() != 1 {
		t.Fatalf("breaches %d, want 1", Breaches())
	}
	snap := ResourcePolicySnapshot()
	if snap.AllocationBreaches != 1 || snap.MaxSingleAllocation != DefaultMaxSingleAllocation {
		t.Fatalf("snapshot %+v", snap)
	}

	// End to end: a small cap makes a read that would materialise the symbol array
	// breach the policy — the counter increments and no huge allocation happens.
	src := sampleSource(0, 4096)
	ekb, err := WriteBody(1, src, rootProfile())
	if err != nil {
		t.Fatal(err)
	}
	r, err := Open(ekb)
	if err != nil {
		t.Fatal(err)
	}
	SetMaxSingleAllocation(64)
	ResetBreaches()
	if _, err := r.ReadAt(0, uint64(len(src))); err == nil {
		t.Fatal("read under a 64-byte cap succeeded")
	}
	if Breaches() == 0 {
		t.Fatal("breach counter did not increment")
	}
}
