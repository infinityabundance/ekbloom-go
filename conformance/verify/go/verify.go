package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"ekbloomconformance/blake3"

	ekbloom "github.com/infinityabundance/ekbloom-go"
)

// Report summarises a conformance run.
type Report struct {
	Fixtures int
	Checks   int
	Failures []string
}

// OK reports whether every fixture matched its expectation.
func (r Report) OK() bool { return len(r.Failures) == 0 }

// Verify reads manifest.json and every referenced fixture under root, and checks
// the Go implementation against the manifest. It uses only the public ekbloom
// API; it never imports the fixture generator, and it never imports or links the
// Rust implementation.
func Verify(root string, out io.Writer) (Report, error) {
	m, err := LoadManifest(root)
	if err != nil {
		return Report{}, err
	}
	var rep Report
	rep.Fixtures = len(m.Fixtures)
	fail := func(format string, args ...any) {
		rep.Failures = append(rep.Failures, fmt.Sprintf(format, args...))
	}

	for i := range m.Fixtures {
		f := &m.Fixtures[i]
		data, err := os.ReadFile(filepath.Join(root, f.File))
		if err != nil {
			fail("%s: read: %v", f.Name, err)
			continue
		}
		// 1. The fixture file's recorded sha256 must match (immutability).
		sum := sha256.Sum256(data)
		gotSHA := hex.EncodeToString(sum[:])
		rep.Checks++
		if gotSHA != f.SHA256 {
			fail("%s: sha256 %s != manifest %s", f.Name, gotSHA, f.SHA256)
			continue
		}

		if f.Polarity == "positive" {
			if err := verifyPositive(root, f, data, &rep, fail); err != nil {
				fail("%s: %v", f.Name, err)
			}
		} else {
			verifyNegative(root, f, data, &rep, fail)
		}
	}
	return rep, nil
}

func verifyPositive(root string, f *Fixture, data []byte, rep *Report, fail func(string, ...any)) error {
	r, err := openFixture(root, f, data)
	if err != nil {
		return fmt.Errorf("open: %v", err)
	}
	if f.Expected.MachineID != nil {
		rep.Checks++
		if got := r.MachineID(); got != *f.Expected.MachineID {
			fail("%s: machine_id %d != %d", f.Name, got, *f.Expected.MachineID)
		}
	}
	if f.Expected.LogicalLen != nil {
		rep.Checks++
		if got := r.LogicalLen(); got != *f.Expected.LogicalLen {
			fail("%s: logical_len %d != %d", f.Name, got, *f.Expected.LogicalLen)
		}
	}
	if f.Expected.PhysicalBytes != nil {
		rep.Checks++
		if got := r.PhysicalBytes(); got != *f.Expected.PhysicalBytes {
			fail("%s: physical_bytes %d != %d", f.Name, got, *f.Expected.PhysicalBytes)
		}
	}
	if f.Expected.Blake3OfDecoded != nil {
		rep.Checks++
		n := r.LogicalLen()
		decoded, err := r.ReadAt(0, n)
		if err != nil {
			fail("%s: ReadAt: %v", f.Name, err)
		} else {
			h := blake3.Sum(decoded)
			if got := hex.EncodeToString(h[:]); got != *f.Expected.Blake3OfDecoded {
				fail("%s: decoded blake3 %s != %s", f.Name, got, *f.Expected.Blake3OfDecoded)
			}
		}
	}
	if f.Expected.WorstCaseSteps != nil {
		rep.Checks++
		n := r.LogicalLen()
		got, err := r.WorstCaseStepsFor(0, n)
		if err != nil {
			fail("%s: WorstCaseStepsFor: %v", f.Name, err)
		} else if got != *f.Expected.WorstCaseSteps {
			fail("%s: worst_case_steps %d != %d", f.Name, got, *f.Expected.WorstCaseSteps)
		}
	}
	return nil
}

func verifyNegative(root string, f *Fixture, data []byte, rep *Report, fail func(string, ...any)) {
	rep.Checks++
	_, err := openFixture(root, f, data)
	if err == nil {
		fail("%s: expected rejection (%s) but Open succeeded", f.Name, *f.Expected.ErrorKind)
		return
	}
	got := kindName(err)
	accept := map[string]bool{}
	if f.Expected.ErrorKind != nil {
		accept[*f.Expected.ErrorKind] = true
	}
	for _, k := range f.Expected.ErrorKindAccept {
		accept[k] = true
	}
	if !accept[got] {
		fail("%s: rejected with %s, want %s (accept %v)", f.Name, got, *f.Expected.ErrorKind, f.Expected.ErrorKindAccept)
	}
}

// openFixture opens a fixture through the public API, registering any shared
// dictionary and applying the authenticated entry point when the manifest says
// so.
func openFixture(root string, f *Fixture, data []byte) (*ekbloom.Reader, error) {
	var dict *ekbloom.Dictionary
	if f.DictFile != nil {
		db, err := os.ReadFile(filepath.Join(root, *f.DictFile))
		if err != nil {
			return nil, fmt.Errorf("read dict: %w", err)
		}
		d, err := ekbloom.DictionaryFromBytes(db)
		if err != nil {
			return nil, fmt.Errorf("parse dict: %w", err)
		}
		dict = &d
	}
	if f.AuthKeyHex != nil {
		key, err := parseKey(*f.AuthKeyHex)
		if err != nil {
			return nil, err
		}
		return ekbloom.OpenAuthenticated(data, key)
	}
	if dict != nil {
		return ekbloom.OpenWithDict(data, *dict)
	}
	return ekbloom.Open(data)
}

func parseKey(hexKey string) ([32]byte, error) {
	var key [32]byte
	b, err := hex.DecodeString(hexKey)
	if err != nil {
		return key, fmt.Errorf("auth key hex: %w", err)
	}
	if len(b) != 32 {
		return key, fmt.Errorf("auth key must be 32 bytes, got %d", len(b))
	}
	copy(key[:], b)
	return key, nil
}

// kindName maps a typed ekbloom error to its stable SPEC taxonomy name.
func kindName(err error) string {
	var e *ekbloom.Error
	if errors.As(err, &e) {
		switch e.Kind {
		case ekbloom.KindBadMagic:
			return "ErrBadMagic"
		case ekbloom.KindUnsupportedVersion:
			return "ErrUnsupportedVersion"
		case ekbloom.KindUnsupportedCoreVersion:
			return "ErrUnsupportedCoreVersion"
		case ekbloom.KindUnsupportedManifestVersion:
			return "ErrUnsupportedManifestVersion"
		case ekbloom.KindTruncated:
			return "ErrTruncated"
		case ekbloom.KindChecksumMismatch:
			return "ErrChecksumMismatch"
		case ekbloom.KindCorrupt:
			return "ErrCorrupt"
		case ekbloom.KindBadSection:
			return "ErrBadSection"
		case ekbloom.KindOverflow:
			return "ErrOverflow"
		case ekbloom.KindUnknownMachine:
			return "ErrUnknownMachine"
		case ekbloom.KindCoreTooShort:
			return "ErrCoreTooShort"
		case ekbloom.KindOutOfBounds:
			return "ErrOutOfBounds"
		case ekbloom.KindAllocationTooLarge:
			return "ErrAllocationTooLarge"
		case ekbloom.KindNonTermination:
			return "ErrNonTermination"
		case ekbloom.KindInvalidArgument:
			return "ErrInvalidArgument"
		case ekbloom.KindRecoveryFailed:
			return "ErrRecoveryFailed"
		case ekbloom.KindNotCertified:
			return "ErrNotCertified"
		case ekbloom.KindBadInterval:
			return "ErrBadInterval"
		case ekbloom.KindBadDepth:
			return "ErrBadDepth"
		}
		return fmt.Sprintf("ErrUnknownKind(%d)", int(e.Kind))
	}
	return "non-conforming error: " + err.Error()
}
