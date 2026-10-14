package ekbloom

import "github.com/infinityabundance/ekbloom-go/internal/ekb"

// ProfileKind selects the manifestation profile applied to a body.
type ProfileKind = ekb.ProfileKind

// Profile is a manifestation profile (kind plus its parameters).
type Profile = ekb.Profile

// SectionKind identifies a section of the wire format.
type SectionKind = ekb.SectionKind

// Policy selects the streaming representation policy.
type Policy = ekb.Policy

// TileDirectoryKind selects the tile directory layout.
type TileDirectoryKind = ekb.TileDirectoryKind

// Profile kinds.
const (
	ProfileRoot     = ekb.ProfileRoot
	ProfileSparse   = ekb.ProfileSparse
	ProfileDense    = ekb.ProfileDense
	ProfileFull     = ekb.ProfileFull
	ProfileBloom    = ekb.ProfileBloom
	ProfileRegional = ekb.ProfileRegional
	ProfilePath     = ekb.ProfilePath
	ProfileMaxSpeed = ekb.ProfileMaxSpeed
)

// Section kinds.
const (
	SectionCoreMachine      = ekb.SectionCoreMachine
	SectionHistoryFull      = ekb.SectionHistoryFull
	SectionCheckpoints      = ekb.SectionCheckpoints
	SectionBloomGlobal      = ekb.SectionBloomGlobal
	SectionBloomContext     = ekb.SectionBloomContext
	SectionBloomRegion      = ekb.SectionBloomRegion
	SectionBloomPath        = ekb.SectionBloomPath
	SectionCertificate      = ekb.SectionCertificate
	SectionManifest         = ekb.SectionManifest
	SectionAuth             = ekb.SectionAuth
	SectionHistorySegmented = ekb.SectionHistorySegmented
	SectionTiles            = ekb.SectionTiles
	SectionBlocks           = ekb.SectionBlocks
)

// Machine identifiers.
const (
	MachineNL2     = ekb.MachineNL2
	MachineRans0   = ekb.MachineRans0
	MachineStore   = ekb.MachineStore
	MachineRans0I  = ekb.MachineRans0I
	MachinePack    = ekb.MachinePack
	MachineSeg     = ekb.MachineSeg
	MachineRans0W  = ekb.MachineRans0W
	MachineRans0IL = ekb.MachineRans0IL
	MachineDict    = ekb.MachineDict
)

// Streaming policy and defaults.
const (
	StreamDefaultBlockBytes = ekb.StreamDefaultBlockBytes
	PolicyAuto              = ekb.PolicyAuto
	PolicyPacked            = ekb.PolicyPacked
	PolicySegmented         = ekb.PolicySegmented
	PolicyWord16            = ekb.PolicyWord16
	PolicyIndependentLanes  = ekb.PolicyIndependentLanes
)

// Tile directory kinds.
const (
	TileFlat          = ekb.TileFlat
	TileTwoLevelRadix = ekb.TileTwoLevelRadix
)

// DefaultMaxSingleAllocation is the default cap on any whole-object allocation
// driven by a declared length (1 GiB).
const DefaultMaxSingleAllocation = ekb.DefaultMaxSingleAllocation

// EstimatePhysicalBytesForCore is the static upper-bound estimate of the physical
// bytes for a core of coreLen bytes encoding sourceLen logical bytes under p.
func EstimatePhysicalBytesForCore(coreLen, sourceLen uint64, p Profile) uint64 {
	return ekb.EstimatePhysicalBytesForCore(coreLen, sourceLen, p)
}
