package ekbloom

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ResourceMetrics is a snapshot of the process-wide resource policy, mirroring
// the metrics the reference exports (the allocation cap and the breach counter).
type ResourceMetrics struct {
	MaxSingleAllocation uint64 `json:"max_single_allocation"`
	AllocationBreaches  uint64 `json:"allocation_breaches"`
}

// ResourcePolicySnapshot returns the current resource-policy metrics.
func ResourcePolicySnapshot() ResourceMetrics {
	return ResourceMetrics{
		MaxSingleAllocation: MaxSingleAllocation(),
		AllocationBreaches:  Breaches(),
	}
}

// ReaderMetrics is a per-object accounting snapshot suitable for export.
type ReaderMetrics struct {
	MachineID      uint32  `json:"machine_id"`
	LogicalLen     uint64  `json:"logical_len"`
	VisibleBytes   uint64  `json:"visible_bytes"`
	PhysicalBytes  uint64  `json:"physical_bytes"`
	ConcealedBytes uint64  `json:"concealed_bytes"`
	ExpansionRatio float64 `json:"expansion_ratio"` // E_V = physical / visible
	Generation     uint64  `json:"generation"`
}

// Metrics returns the per-object metrics for this Reader.
func (r *Reader) Metrics() ReaderMetrics {
	visible := uint64(len(r.core))
	ratio := 0.0
	if visible != 0 {
		ratio = float64(r.physicalBytes) / float64(visible)
	}
	concealed := uint64(0)
	if r.physicalBytes > visible {
		concealed = r.physicalBytes - visible
	}
	return ReaderMetrics{
		MachineID:      r.header.CoreMachineID,
		LogicalLen:     r.logical,
		VisibleBytes:   visible,
		PhysicalBytes:  r.physicalBytes,
		ConcealedBytes: concealed,
		ExpansionRatio: ratio,
		Generation:     r.generation,
	}
}

// WorstCaseMetric pairs a request with its certified step bound, so a runbook can
// export `worst_case_steps_for` alongside the read metrics.
func (r *Reader) WorstCaseMetric(off, length uint64) (uint64, error) {
	return r.WorstCaseStepsFor(off, length)
}

// MetricsText renders the resource-policy metrics plus an optional per-object
// snapshot in a Prometheus-style exposition format (one `name value` per line).
func MetricsText(r *Reader) string {
	var b strings.Builder
	rm := ResourcePolicySnapshot()
	fmt.Fprintf(&b, "ekbloom_resource_max_single_allocation %d\n", rm.MaxSingleAllocation)
	fmt.Fprintf(&b, "ekbloom_resource_allocation_breaches %d\n", rm.AllocationBreaches)
	if r != nil {
		m := r.Metrics()
		fmt.Fprintf(&b, "ekbloom_physical_bytes %d\n", m.PhysicalBytes)
		fmt.Fprintf(&b, "ekbloom_visible_bytes %d\n", m.VisibleBytes)
		fmt.Fprintf(&b, "ekbloom_expansion_ratio %g\n", m.ExpansionRatio)
		fmt.Fprintf(&b, "ekbloom_logical_len %d\n", m.LogicalLen)
		fmt.Fprintf(&b, "ekbloom_generation %d\n", m.Generation)
	}
	return b.String()
}

// MetricsJSON renders the resource-policy metrics (and, if r is non-nil, the
// per-object metrics) as a single JSON object.
func MetricsJSON(r *Reader) ([]byte, error) {
	obj := map[string]any{"resource": ResourcePolicySnapshot()}
	if r != nil {
		obj["object"] = r.Metrics()
	}
	return json.MarshalIndent(obj, "", "  ")
}
