// Package ekbloom is a dependency-free, pure-Go implementation of the EKBLOOM
// concealed-expansion codec. It is a native port of the Rust `ekbloom` crate and
// is **wire-compatible** with it: a `.ekb` file written by the Rust implementation
// is read byte-for-byte identically here (see `testdata/` and `compat_test.go`).
//
// The implementation contains no cgo and no `unsafe` in the core format, machine
// or reader paths. Every failure is a typed error; the library never panics on
// untrusted input.
//
// The public entry points are Open, Reader.ByteAt, Reader.ReadAt,
// Reader.PhysicalBytes, Reader.Estimate and Reader.WorstCaseStepsFor.
package ekbloom

import (
	"strconv"
)

// Kind classifies an Error so callers can branch on it without string matching.
type Kind int

// Error kinds, mirroring the Rust `EkbloomError` variants.
const (
	KindBadMagic Kind = iota
	KindUnsupportedVersion
	KindUnsupportedCoreVersion
	KindUnsupportedManifestVersion
	KindTruncated
	KindChecksumMismatch
	KindCorrupt
	KindBadSection
	KindOverflow
	KindUnknownMachine
	KindCoreTooShort
	KindOutOfBounds
	KindAllocationTooLarge
	KindNonTermination
	KindInvalidArgument
	KindRecoveryFailed
	KindNotCertified
	KindBadInterval
)

// Error is the single typed error returned by this package. It carries the
// fields relevant to its Kind; unused fields are zero. Use errors.Is with the
// exported sentinels (ErrBadMagic, ErrCorrupt, …) or inspect Kind directly.
type Error struct {
	Kind Kind

	// Section and Offset locate a corruption (KindCorrupt, KindChecksumMismatch).
	Section string
	Offset  uint64

	// Version fields (KindUnsupportedVersion, …).
	Major       uint16
	Minor       uint16
	Version     uint32
	MachineID   uint32
	CoreVersion uint16

	// Length fields.
	Needed    uint64
	Available uint64
	Requested uint64
	Limit     uint64

	// Offset/Length/LogicalLen describe an out-of-bounds read.
	Offset2    uint64
	Length     uint64
	LogicalLen uint64

	// Reason is a human-readable detail for otherwise-unstructured failures.
	Reason string
}

func (e *Error) Error() string {
	switch e.Kind {
	case KindBadMagic:
		return "bad magic: not an EKBLOOM file"
	case KindUnsupportedVersion:
		return "unsupported format version " + strconv.Itoa(int(e.Major)) + "." + strconv.Itoa(int(e.Minor))
	case KindUnsupportedCoreVersion:
		return "unsupported core version " + strconv.Itoa(int(e.CoreVersion)) +
			" for machine " + strconv.FormatUint(uint64(e.MachineID), 10)
	case KindUnsupportedManifestVersion:
		return "unsupported manifest record version " + strconv.FormatUint(uint64(e.Version), 10)
	case KindTruncated:
		return "truncated: needed " + strconv.FormatUint(e.Needed, 10) +
			" bytes, " + strconv.FormatUint(e.Available, 10) + " available"
	case KindChecksumMismatch:
		return "checksum mismatch in section " + e.Section
	case KindCorrupt:
		return "corrupt " + e.Section + " at logical offset " + strconv.FormatUint(e.Offset, 10)
	case KindBadSection:
		return "bad section: " + e.Reason
	case KindOverflow:
		return "integer overflow: " + e.Reason
	case KindUnknownMachine:
		return "unknown machine id " + strconv.FormatUint(uint64(e.MachineID), 10)
	case KindCoreTooShort:
		return "core too short: need " + strconv.FormatUint(e.Needed, 10) +
			" bytes, have " + strconv.FormatUint(e.Available, 10)
	case KindOutOfBounds:
		return "read out of bounds: offset " + strconv.FormatUint(e.Offset2, 10) +
			", length " + strconv.FormatUint(e.Length, 10) +
			", logical_len " + strconv.FormatUint(e.LogicalLen, 10)
	case KindAllocationTooLarge:
		return "allocation " + strconv.FormatUint(e.Requested, 10) +
			" exceeds limit " + strconv.FormatUint(e.Limit, 10)
	case KindNonTermination:
		return "aborted: transition data risks nontermination"
	case KindInvalidArgument:
		return "invalid argument: " + e.Reason
	case KindRecoveryFailed:
		return "recovery failed: " + e.Reason
	case KindNotCertified:
		return e.Section + " is not certified"
	case KindBadInterval:
		return "unsupported checkpoint interval " + strconv.FormatUint(e.Offset, 10)
	default:
		return "ekbloom error"
	}
}

// Is reports whether target is the same error kind, so errors.Is works with the
// exported sentinels.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	if !ok {
		return false
	}
	return t.Kind == e.Kind
}

// Sentinel errors. Compare with errors.Is(err, ekbloom.ErrX). For KindCorrupt the
// sentinel matches any corruption; inspect the *Error to read Section/Offset.
var (
	ErrBadMagic                   = &Error{Kind: KindBadMagic}
	ErrUnsupportedVersion         = &Error{Kind: KindUnsupportedVersion}
	ErrUnsupportedCoreVersion     = &Error{Kind: KindUnsupportedCoreVersion}
	ErrUnsupportedManifestVersion = &Error{Kind: KindUnsupportedManifestVersion}
	ErrTruncated                  = &Error{Kind: KindTruncated}
	ErrChecksumMismatch           = &Error{Kind: KindChecksumMismatch}
	ErrCorrupt                    = &Error{Kind: KindCorrupt}
	ErrBadSection                 = &Error{Kind: KindBadSection}
	ErrOverflow                   = &Error{Kind: KindOverflow}
	ErrUnknownMachine             = &Error{Kind: KindUnknownMachine}
	ErrCoreTooShort               = &Error{Kind: KindCoreTooShort}
	ErrOutOfBounds                = &Error{Kind: KindOutOfBounds}
	ErrAllocationTooLarge         = &Error{Kind: KindAllocationTooLarge}
	ErrNonTermination             = &Error{Kind: KindNonTermination}
	ErrInvalidArgument            = &Error{Kind: KindInvalidArgument}
	ErrRecoveryFailed             = &Error{Kind: KindRecoveryFailed}
	ErrNotCertified               = &Error{Kind: KindNotCertified}
	ErrBadInterval                = &Error{Kind: KindBadInterval}
)

func badSection(reason string) *Error { return &Error{Kind: KindBadSection, Reason: reason} }
func truncated(needed, available uint64) *Error {
	return &Error{Kind: KindTruncated, Needed: needed, Available: available}
}
func badMagic() *Error { return &Error{Kind: KindBadMagic} }
func overflow(what string) *Error {
	return &Error{Kind: KindOverflow, Reason: what}
}
func corrupt(section string, offset uint64) *Error {
	return &Error{Kind: KindCorrupt, Section: section, Offset: offset}
}
func outOfBounds(offset, length, logicalLen uint64) *Error {
	return &Error{Kind: KindOutOfBounds, Offset2: offset, Length: length, LogicalLen: logicalLen}
}
func coreTooShort(needed, available uint64) *Error {
	return &Error{Kind: KindCoreTooShort, Needed: needed, Available: available}
}

// CorruptDetail returns the section and offset of a corruption if err is one.
func CorruptDetail(err error) (section string, offset uint64, ok bool) {
	if e, isE := err.(*Error); isE && e.Kind == KindCorrupt {
		return e.Section, e.Offset, true
	}
	return "", 0, false
}
