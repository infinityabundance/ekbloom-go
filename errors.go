package ekbloom

import "github.com/infinityabundance/ekbloom-go/internal/ekb"

// Error is the single typed error returned by this package.
type Error = ekb.Error

// Kind classifies an Error so callers can branch on it without string matching.
type Kind = ekb.Kind

// Error kinds, mirroring the Rust `EkbloomError` variants.
const (
	KindBadMagic                   = ekb.KindBadMagic
	KindUnsupportedVersion         = ekb.KindUnsupportedVersion
	KindUnsupportedCoreVersion     = ekb.KindUnsupportedCoreVersion
	KindUnsupportedManifestVersion = ekb.KindUnsupportedManifestVersion
	KindTruncated                  = ekb.KindTruncated
	KindChecksumMismatch           = ekb.KindChecksumMismatch
	KindCorrupt                    = ekb.KindCorrupt
	KindBadSection                 = ekb.KindBadSection
	KindOverflow                   = ekb.KindOverflow
	KindUnknownMachine             = ekb.KindUnknownMachine
	KindCoreTooShort               = ekb.KindCoreTooShort
	KindOutOfBounds                = ekb.KindOutOfBounds
	KindAllocationTooLarge         = ekb.KindAllocationTooLarge
	KindNonTermination             = ekb.KindNonTermination
	KindInvalidArgument            = ekb.KindInvalidArgument
	KindRecoveryFailed             = ekb.KindRecoveryFailed
	KindNotCertified               = ekb.KindNotCertified
	KindBadInterval                = ekb.KindBadInterval
	KindBadDepth                   = ekb.KindBadDepth
)

// Sentinel errors. Compare with errors.Is(err, ekbloom.ErrX). For KindCorrupt the
// sentinel matches any corruption; inspect the *Error to read Section/Offset.
var (
	ErrBadMagic                   = ekb.ErrBadMagic
	ErrUnsupportedVersion         = ekb.ErrUnsupportedVersion
	ErrUnsupportedCoreVersion     = ekb.ErrUnsupportedCoreVersion
	ErrUnsupportedManifestVersion = ekb.ErrUnsupportedManifestVersion
	ErrTruncated                  = ekb.ErrTruncated
	ErrChecksumMismatch           = ekb.ErrChecksumMismatch
	ErrCorrupt                    = ekb.ErrCorrupt
	ErrBadSection                 = ekb.ErrBadSection
	ErrOverflow                   = ekb.ErrOverflow
	ErrUnknownMachine             = ekb.ErrUnknownMachine
	ErrCoreTooShort               = ekb.ErrCoreTooShort
	ErrOutOfBounds                = ekb.ErrOutOfBounds
	ErrAllocationTooLarge         = ekb.ErrAllocationTooLarge
	ErrNonTermination             = ekb.ErrNonTermination
	ErrInvalidArgument            = ekb.ErrInvalidArgument
	ErrRecoveryFailed             = ekb.ErrRecoveryFailed
	ErrNotCertified               = ekb.ErrNotCertified
	ErrBadInterval                = ekb.ErrBadInterval
	ErrBadDepth                   = ekb.ErrBadDepth
)

// CorruptDetail extracts a corruption's section and absolute offset from err, if
// err is (or wraps) a localized corruption.
func CorruptDetail(err error) (section string, offset uint64, ok bool) {
	return ekb.CorruptDetail(err)
}
