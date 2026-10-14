package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"ekbloomconformance/blake3"
)

// conformanceRoot is the conformance/ directory, resolved relative to this
// package (conformance/verify/go).
const conformanceRoot = "../.."

// TestManifestWellFormed proves the manifest itself is well-formed and that
// every fixture file's recorded sha256 matches its bytes on disk. This is the
// immutability check: a fixture that drifts from the manifest fails here.
func TestManifestWellFormed(t *testing.T) {
	m, err := LoadManifest(conformanceRoot)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if !m.Immutable {
		t.Error("manifest must declare itself immutable")
	}
	if m.Authority == "" {
		t.Error("manifest must name its authority (SPEC.md)")
	}
	if len(m.Fixtures) < 40 {
		t.Fatalf("want at least 40 fixtures, got %d", len(m.Fixtures))
	}

	names := map[string]bool{}
	for _, f := range m.Fixtures {
		if names[f.Name] {
			t.Errorf("duplicate fixture name %q", f.Name)
		}
		names[f.Name] = true

		data, err := os.ReadFile(filepath.Join(conformanceRoot, f.File))
		if err != nil {
			t.Errorf("%s: read %s: %v", f.Name, f.File, err)
			continue
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != f.SHA256 {
			t.Errorf("%s: sha256 %s != manifest %s", f.Name, got, f.SHA256)
		}

		switch f.Polarity {
		case "positive":
			if f.Expected.LogicalLen == nil || f.Expected.Blake3OfDecoded == nil || f.Expected.PhysicalBytes == nil {
				t.Errorf("%s: positive fixture missing an expectation", f.Name)
			}
			if f.Expected.ErrorKind != nil {
				t.Errorf("%s: positive fixture must not expect an error", f.Name)
			}
		case "negative":
			if f.Expected.ErrorKind == nil || *f.Expected.ErrorKind == "" {
				t.Errorf("%s: negative fixture needs error_kind", f.Name)
			}
			if f.Expected.LogicalLen != nil || f.Expected.Blake3OfDecoded != nil {
				t.Errorf("%s: negative fixture must not expect decoded bytes", f.Name)
			}
		default:
			t.Errorf("%s: bad polarity %q", f.Name, f.Polarity)
		}

		for field, src := range map[string]string{
			"machine_id": f.Source.MachineID, "logical_len": f.Source.LogicalLen,
			"physical_bytes": f.Source.PhysicalBytes, "blake3_of_decoded": f.Source.Blake3OfDecoded,
			"error_kind": f.Source.ErrorKind, "worst_case_steps": f.Source.WorstCaseSteps,
		} {
			if src != "derived" && src != "asserted" && src != "" {
				t.Errorf("%s: expectation_source.%s = %q (want derived|asserted)", f.Name, field, src)
			}
		}
	}

	// Every kind named anywhere must be a member of the SPEC taxonomy.
	taxonomy := map[string]bool{
		"ErrBadMagic": true, "ErrUnsupportedVersion": true, "ErrUnsupportedCoreVersion": true,
		"ErrUnsupportedManifestVersion": true, "ErrTruncated": true, "ErrChecksumMismatch": true,
		"ErrCorrupt": true, "ErrBadSection": true, "ErrOverflow": true, "ErrUnknownMachine": true,
		"ErrCoreTooShort": true, "ErrOutOfBounds": true, "ErrAllocationTooLarge": true,
		"ErrNonTermination": true, "ErrInvalidArgument": true, "ErrRecoveryFailed": true,
		"ErrNotCertified": true, "ErrBadInterval": true, "ErrBadDepth": true,
	}
	for _, f := range m.Fixtures {
		if f.Expected.ErrorKind != nil && !taxonomy[*f.Expected.ErrorKind] {
			t.Errorf("%s: error_kind %q is not in the SPEC taxonomy", f.Name, *f.Expected.ErrorKind)
		}
		for _, k := range f.Expected.ErrorKindAccept {
			if !taxonomy[k] {
				t.Errorf("%s: accept kind %q is not in the SPEC taxonomy", f.Name, k)
			}
		}
	}
	for _, d := range m.Divergences {
		if !names[d.Fixture] {
			t.Errorf("divergence references unknown fixture %q", d.Fixture)
		}
	}
}

// TestAllFixturesConform runs the full conformance check against the manifest.
func TestAllFixturesConform(t *testing.T) {
	rep, err := Verify(conformanceRoot, io.Discard)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	for _, f := range rep.Failures {
		t.Error(f)
	}
	if rep.Fixtures < 40 {
		t.Errorf("want at least 40 fixtures, got %d", rep.Fixtures)
	}
}

// TestVendoredBlake3MatchesReference pins the vendored BLAKE3 primitive against
// the Rust-`blake3`-generated vectors in testdata/blake3_vectors.json. The
// decoded-bytes expectation is a BLAKE3 digest; this test removes any
// circularity that would arise if the verifier's own hash primitive were wrong
// in the same way the implementation's were.
func TestVendoredBlake3MatchesReference(t *testing.T) {
	path := "../../../testdata/blake3_vectors.json"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("vectors unavailable: %v", err)
	}
	var f struct {
		Pattern string `json:"pattern"`
		KeyHex  string `json:"key_hex"`
		Vectors []struct {
			Len   int    `json:"len"`
			Hash  string `json:"hash"`
			Keyed string `json:"keyed"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}
	if f.Pattern != "data[i] = (i*7 + 11) & 0xff" {
		t.Fatalf("unexpected vector pattern %q", f.Pattern)
	}
	var key [32]byte
	kb, err := hex.DecodeString(f.KeyHex)
	if err != nil || len(kb) != 32 {
		t.Fatalf("bad key hex")
	}
	copy(key[:], kb)
	for _, v := range f.Vectors {
		data := make([]byte, v.Len)
		for i := range data {
			data[i] = byte((i*7 + 11) & 0xff)
		}
		h := blake3.Sum(data)
		if got := hex.EncodeToString(h[:]); got != v.Hash {
			t.Errorf("len=%d blake3 %s != %s", v.Len, got, v.Hash)
		}
		kh := blake3.SumKeyed(key, data)
		if got := hex.EncodeToString(kh[:]); got != v.Keyed {
			t.Errorf("len=%d keyed blake3 %s != %s", v.Len, got, v.Keyed)
		}
	}
}
