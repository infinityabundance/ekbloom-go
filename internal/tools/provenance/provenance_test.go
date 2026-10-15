package provenance

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestStampPreservesExistingBytes is the guard for the "deterministic fields are
// byte-identical" requirement: stamping must only add a trailing provenance key,
// never reformat or reorder what was already there.
func TestStampPreservesExistingBytes(t *testing.T) {
	in := []byte("{\n  \"samples\": 200000,\n  \"rows\": [\n    {\n      \"x\": 1\n    }\n  ]\n}\n")
	out := Stamp(in)

	if !json.Valid(out) {
		t.Fatalf("stamp produced invalid JSON:\n%s", out)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := m["provenance"]; !ok {
		t.Fatal("stamp did not add a provenance key")
	}
	if _, ok := m["samples"]; !ok {
		t.Fatal("stamp dropped the samples key")
	}
	if !strings.HasSuffix(string(out), "\n") {
		t.Fatal("stamp result does not end in a newline")
	}
	const wantPrefix = "{\n  \"samples\": 200000,\n  \"rows\": [\n    {\n      \"x\": 1\n    }\n  ],\n  \"provenance\": "
	if !strings.HasPrefix(string(out), wantPrefix) {
		t.Fatalf("existing bytes changed.\n got prefix: %q\nwant prefix: %q", string(out), wantPrefix)
	}
}

func TestStampEmptyObject(t *testing.T) {
	out := StampWith([]byte("{}"), Provenance{Go: "go1.23"})
	if !json.Valid(out) {
		t.Fatalf("stamp of {} produced invalid JSON:\n%s", out)
	}
	if !strings.Contains(string(out), `"provenance"`) {
		t.Fatalf("no provenance key in %q", out)
	}
}

func TestStampNonObjectUnchanged(t *testing.T) {
	for _, in := range []string{"[]\n", "42\n", "", "  \n"} {
		if got := StampWith([]byte(in), Provenance{Go: "go1.23"}); string(got) != in {
			t.Fatalf("non-object input changed: %q -> %q", in, got)
		}
	}
}

func TestCurrentHasLoadAvgAndIdle(t *testing.T) {
	p := Current()
	if p.Go == "" {
		t.Fatal("empty go version")
	}
	if p.EKBRuns < 1 {
		t.Fatalf("ekb_runs = %d, want >= 1", p.EKBRuns)
	}
	if p.Note == "" {
		t.Fatal("empty note")
	}
}
