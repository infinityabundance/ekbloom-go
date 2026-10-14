package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Expected is the field-by-field expectation for one fixture. Any field may be
// null when the expectation does not apply (e.g. physical_bytes for a fixture
// that must not open). The suite is implementation-neutral: no expectation may
// depend on which verifier is reading it.
type Expected struct {
	// MachineID is the core machine id the implementation must report.
	MachineID *uint32 `json:"machine_id"`
	// LogicalLen is the number of logical symbols N.
	LogicalLen *uint64 `json:"logical_len"`
	// PhysicalBytes is the manifested physical accounting (core + structures).
	PhysicalBytes *uint64 `json:"physical_bytes"`
	// Blake3OfDecoded is the hex BLAKE3 of the fully decoded logical bytes.
	Blake3OfDecoded *string `json:"blake3_of_decoded"`
	// ErrorKind is the canonical error-kind name an implementation must return
	// for a negative fixture (nil for positives).
	ErrorKind *string `json:"error_kind"`
	// ErrorKindAccept lists additional kind names that are also an acceptable
	// rejection for this fixture. It exists only for the (documented) cases
	// where two conforming implementations surface a different *typed* error
	// for the same malformed input while still rejecting it.
	ErrorKindAccept []string `json:"error_kind_accept"`
	// WorstCaseSteps is the resource bound for WorstCaseStepsFor(0, N).
	WorstCaseSteps *uint64 `json:"worst_case_steps"`
}

// ExpectationSource records, per field, whether the expectation is "derived"
// (from SPEC.md alone, without running a decoder) or "asserted" (recorded from a
// run of a reference implementation). The distinction is deliberately visible.
type ExpectationSource struct {
	MachineID       string `json:"machine_id"`
	LogicalLen      string `json:"logical_len"`
	PhysicalBytes   string `json:"physical_bytes"`
	Blake3OfDecoded string `json:"blake3_of_decoded"`
	ErrorKind       string `json:"error_kind"`
	WorstCaseSteps  string `json:"worst_case_steps"`
}

// Fixture is one immutable golden file plus its expectations.
type Fixture struct {
	Name       string            `json:"name"`
	File       string            `json:"file"`
	SHA256     string            `json:"sha256"`
	Origin     string            `json:"origin"`   // "rust" | "go" | "negative"
	Polarity   string            `json:"polarity"` // "positive" | "negative"
	SrcFile    *string           `json:"src_file"`
	DictFile   *string           `json:"dict_file"`
	AuthKeyHex *string           `json:"auth_key_hex"`
	Mutation   *string           `json:"mutation"`
	Expected   Expected          `json:"expected"`
	Source     ExpectationSource `json:"expectation_source"`
}

// Divergence documents a known, accepted cross-implementation difference in the
// typed error surfaced for a malformed input. It is informational: both kinds
// are in the fixture's accept set.
type Divergence struct {
	Fixture string `json:"fixture"`
	Go      string `json:"go"`
	Rust    string `json:"rust"`
	Note    string `json:"note"`
}

// Manifest is the versioned, immutable golden index.
type Manifest struct {
	FormatVersion string       `json:"format_version"`
	Authority     string       `json:"authority"`
	Immutable     bool         `json:"immutable"`
	Note          string       `json:"note"`
	Fixtures      []Fixture    `json:"fixtures"`
	Divergences   []Divergence `json:"divergences"`
}

// LoadManifest reads and structurally validates manifest.json under root
// (the conformance/ directory).
func LoadManifest(root string) (*Manifest, error) {
	path := filepath.Join(root, "manifest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var m Manifest
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if m.FormatVersion == "" {
		return nil, fmt.Errorf("manifest: format_version is required")
	}
	if len(m.Fixtures) == 0 {
		return nil, fmt.Errorf("manifest: no fixtures")
	}
	seen := map[string]bool{}
	for i := range m.Fixtures {
		f := &m.Fixtures[i]
		if f.Name == "" || f.File == "" || f.SHA256 == "" {
			return nil, fmt.Errorf("manifest: fixture[%d] missing name/file/sha256", i)
		}
		if seen[f.Name] {
			return nil, fmt.Errorf("manifest: duplicate fixture name %q", f.Name)
		}
		seen[f.Name] = true
		if f.Polarity != "positive" && f.Polarity != "negative" {
			return nil, fmt.Errorf("manifest: fixture %q has bad polarity %q", f.Name, f.Polarity)
		}
		if len(f.SHA256) != 64 {
			return nil, fmt.Errorf("manifest: fixture %q sha256 is not 64 hex chars", f.Name)
		}
		if f.Polarity == "negative" && (f.Expected.ErrorKind == nil || *f.Expected.ErrorKind == "") {
			return nil, fmt.Errorf("manifest: negative fixture %q needs error_kind", f.Name)
		}
		if f.Polarity == "positive" && f.Expected.ErrorKind != nil {
			return nil, fmt.Errorf("manifest: positive fixture %q must not carry error_kind", f.Name)
		}
	}
	return &m, nil
}
