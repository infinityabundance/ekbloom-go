package ekbloom

import (
	"strconv"
	"strings"
)

// Profile names a manifestable executable structure set.
type ProfileKind int

// Profile kinds.
const (
	ProfileRoot ProfileKind = iota
	ProfileSparse
	ProfileDense
	ProfileFull
	ProfileBloom
	ProfileRegional
	ProfilePath
	ProfileMaxSpeed
)

// Profile is a structured manifestation profile.
type Profile struct {
	Kind        ProfileKind
	Interval    uint32
	Depth       uint16
	RegionBytes uint32
}

func rootProfile() Profile           { return Profile{Kind: ProfileRoot} }
func fullProfile() Profile           { return Profile{Kind: ProfileFull} }
func sparseProfile(k uint32) Profile { return Profile{Kind: ProfileSparse, Interval: k} }
func pathProfile(d uint16) Profile   { return Profile{Kind: ProfilePath, Depth: d} }

// label renders the profile in the same form the Rust implementation records.
func (p Profile) label() string {
	switch p.Kind {
	case ProfileRoot:
		return "root"
	case ProfileFull:
		return "full"
	case ProfileSparse:
		return "sparse(K=" + strconv.FormatUint(uint64(p.Interval), 10) + ")"
	case ProfileDense:
		return "dense(K=" + strconv.FormatUint(uint64(p.Interval), 10) + ")"
	case ProfileBloom:
		return "bloom(D=" + strconv.FormatUint(uint64(p.Depth), 10) + ")"
	case ProfileRegional:
		return "regional(D=" + strconv.FormatUint(uint64(p.Depth), 10) + ",R=" + strconv.FormatUint(uint64(p.RegionBytes), 10) + ")"
	case ProfilePath:
		return "path(D=" + strconv.FormatUint(uint64(p.Depth), 10) + ")"
	case ProfileMaxSpeed:
		return "max-speed"
	}
	return "root"
}

// parseProfileLabel parses a manifest profile label back into a Profile.
func parseProfileLabel(s string) (Profile, bool) {
	switch s {
	case "root":
		return rootProfile(), true
	case "full":
		return fullProfile(), true
	case "max-speed":
		return Profile{Kind: ProfileMaxSpeed}, true
	}
	if strings.HasPrefix(s, "sparse(K=") && strings.HasSuffix(s, ")") {
		if v, err := strconv.ParseUint(s[len("sparse(K="):len(s)-1], 10, 32); err == nil {
			return sparseProfile(uint32(v)), true
		}
	}
	if strings.HasPrefix(s, "dense(K=") && strings.HasSuffix(s, ")") {
		if v, err := strconv.ParseUint(s[len("dense(K="):len(s)-1], 10, 32); err == nil {
			return Profile{Kind: ProfileDense, Interval: uint32(v)}, true
		}
	}
	if strings.HasPrefix(s, "bloom(D=") && strings.HasSuffix(s, ")") {
		if v, err := strconv.ParseUint(s[len("bloom(D="):len(s)-1], 10, 16); err == nil {
			return Profile{Kind: ProfileBloom, Depth: uint16(v)}, true
		}
	}
	if strings.HasPrefix(s, "path(D=") && strings.HasSuffix(s, ")") {
		if v, err := strconv.ParseUint(s[len("path(D="):len(s)-1], 10, 16); err == nil {
			return pathProfile(uint16(v)), true
		}
	}
	if strings.HasPrefix(s, "regional(D=") {
		// regional(D=<depth>,R=<region_bytes>)
		inner := s[len("regional(") : len(s)-1]
		parts := strings.Split(inner, ",")
		if len(parts) == 2 {
			d, err1 := strconv.ParseUint(strings.TrimPrefix(parts[0], "D="), 10, 16)
			r, err2 := strconv.ParseUint(strings.TrimPrefix(parts[1], "R="), 10, 32)
			if err1 == nil && err2 == nil {
				return Profile{Kind: ProfileRegional, Depth: uint16(d), RegionBytes: uint32(r)}, true
			}
		}
	}
	return Profile{}, false
}

// Estimate constants (mirroring the Rust manifest accounting identities).
const (
	pathPayloadPrefixBytes      = 2 + 8 + 8
	universalPayloadPrefixBytes = 2 + 8 + 8 + 4
	universalPresentBytes       = 65536 / 8
	universalTableSlots         = 65536
	regionalPayloadPrefixBytes  = 2 + 8 + 8 + 8 + 8 + 4 + 8
	regionalPerEntryExtra       = 2 + 4
	checkpointEntryBytes        = 2
)

func certificateBytes(kind ProfileKind) uint64 {
	var strings_ uint64
	switch kind {
	case ProfilePath:
		strings_ = uint64(len("block-ordinal") + len("block-index") + len("exact-by-construction") + len("every-logical-position"))
	case ProfileRegional:
		strings_ = uint64(len("x16-within-region") + len("region-local") + len("region-exhaustive") + len("every-region-state"))
	case ProfileBloom:
		strings_ = uint64(len("x16") + len("none") + len("state-universe-scan") + len("observed-window-states"))
	default:
		strings_ = 0
	}
	const fixed = 4 + 2 + 4 + 8 + 4 + 32 + 32 + 32 + 4*8
	return fixed + strings_
}

func historyBytesOf(logicalLen uint64) uint64 {
	return (logicalLen + 1) * 4
}

func checkpointBytesOf(logicalLen uint64, interval uint32) uint64 {
	if interval == 0 {
		return ^uint64(0)
	}
	return (logicalLen/uint64(interval) + 1) * checkpointEntryBytes
}

func universalBloomBytes(depth uint16) uint64 {
	if depth == 0 {
		return ^uint64(0)
	}
	entry := uint64(depth) + 4
	return universalPayloadPrefixBytes + universalPresentBytes + universalTableSlots*entry
}

func pathBloomBytesOf(logicalLen uint64, depth uint16) uint64 {
	if depth == 0 {
		return ^uint64(0)
	}
	blocks := (logicalLen + uint64(depth) - 1) / uint64(depth)
	return pathPayloadPrefixBytes + logicalLen + blocks*4
}

func regionalBloomBytesOf(logicalLen uint64, depth uint16, regionBytes uint32) uint64 {
	if depth == 0 || regionBytes == 0 {
		return ^uint64(0)
	}
	rb := uint64(regionBytes)
	regions := (logicalLen + rb - 1) / rb
	if regions == 0 {
		return 0
	}
	entries := rb
	if entries > universalTableSlots {
		entries = universalTableSlots
	}
	table := entries * (uint64(depth) + regionalPerEntryExtra)
	perRegion := regionalPayloadPrefixBytes + table + certificateBytes(ProfileRegional)
	return regions * perRegion
}

// EstimatePhysicalBytesForCore is the static upper-bound estimate of the physical
// bytes a body with a visible core of coreLen bytes producing sourceLen symbols
// would occupy under profile p. It never under-states a machine-1 body.
func EstimatePhysicalBytesForCore(coreLen, sourceLen uint64, p Profile) uint64 {
	switch p.Kind {
	case ProfileRoot:
		return coreLen
	case ProfileSparse:
		return coreLen + checkpointBytesOf(sourceLen, p.Interval)
	case ProfileDense:
		return coreLen + historyBytesOf(sourceLen) + checkpointBytesOf(sourceLen, p.Interval)
	case ProfileFull:
		return coreLen + historyBytesOf(sourceLen)
	case ProfileBloom:
		return coreLen + universalBloomBytes(p.Depth) + certificateBytes(ProfileBloom)
	case ProfileRegional:
		return coreLen + regionalBloomBytesOf(sourceLen, p.Depth, p.RegionBytes)
	case ProfilePath:
		return coreLen + pathBloomBytesOf(sourceLen, p.Depth) + certificateBytes(ProfilePath)
	case ProfileMaxSpeed:
		return coreLen + historyBytesOf(sourceLen) + checkpointBytesOf(sourceLen, 1024) +
			pathBloomBytesOf(sourceLen, 4) + certificateBytes(ProfilePath)
	}
	return coreLen
}
