package ekbloom

import (
	"hash/crc32"

	"github.com/infinityabundance/ekbloom-go/internal/blake3"
)

// WriteBody serializes a body for (machineID, source) under profile into a valid
// `.ekb` image (generation 1). The output is byte-for-byte readable by the Rust
// reference implementation (see scripts/roundtrip.sh).
//
// Supported profiles: Root (core + manifest), Full (history projection) and
// Sparse{K} (machine-1 packed restart points). Other profiles return a typed
// InvalidArgument error rather than emitting a partial structure.
func WriteBody(machineID uint32, source []byte, profile Profile) ([]byte, error) {
	return writeBody(machineID, source, profile, nil)
}

// WriteBodyAuthenticated is WriteBody with an opt-in keyed AUTH section covering
// the header, core, padding and every section up to and including the manifest:
// `mac = blake3::keyed_hash(key, blake3(file[0..auth_header_offset]))`. Read it
// back with OpenAuthenticated.
func WriteBodyAuthenticated(machineID uint32, source []byte, profile Profile, key [32]byte) ([]byte, error) {
	return writeBody(machineID, source, profile, &key)
}

func writeBody(machineID uint32, source []byte, profile Profile, key *[32]byte) ([]byte, error) {
	machine, ok := machineByID(machineID)
	if !ok {
		return nil, &Error{Kind: KindUnknownMachine, MachineID: machineID}
	}
	core, err := machine.Encode(source)
	if err != nil {
		return nil, err
	}
	coreHash := blake3Hash(core)
	objectID := deriveObjectID(coreHash)

	var sections []pendingSection
	var components []ComponentReport
	components = append(components, ComponentReport{Name: "core", Kind: "core", Bytes: uint64(len(core))})

	switch profile.Kind {
	case ProfileRoot:
		// no additional sections
	case ProfileFull:
		payload, err := buildHistory(machine, core, uint64(len(source)), 1)
		if err != nil {
			return nil, err
		}
		sections = append(sections, pendingSection{SectionHistoryFull, payload})
		components = append(components, ComponentReport{Name: "history", Kind: "history", Bytes: uint64((len(source) + 1) * 4)})
	case ProfileSparse:
		payload, err := buildCheckpoints(machine, core, uint64(len(source)), profile.Interval)
		if err != nil {
			return nil, err
		}
		sections = append(sections, pendingSection{SectionCheckpoints, payload})
		layout, _ := parseCheckpointLayout(payload)
		components = append(components, ComponentReport{Name: "checkpoint", Kind: "checkpoint", Bytes: layout.count * uint64(layout.entryLen)})
	default:
		return nil, &Error{Kind: KindInvalidArgument, Reason: "writer supports root, full and sparse profiles"}
	}

	manifest := ManifestRecord{
		Version:    manifestRecordVersion,
		Generation: 1,
		LogicalLen: uint64(len(source)),
		ObjectID:   objectID,
		MachineID:  machineID,
		CoreHash:   coreHash,
		Profiles:   []string{profile.label()},
		Components: toManifestComponents(components),
	}
	sections = append(sections, pendingSection{SectionManifest, manifest.encode()})

	header := GenesisHeader{
		FormatMajor:    formatMajor,
		FormatMinor:    formatMinor,
		FeatureFlags:   0,
		ObjectID:       objectID,
		LogicalSymbols: uint64(len(source)),
		LogicalBytes:   uint64(len(source)),
		CoreLen:        uint64(len(core)),
		CoreMachineID:  machineID,
		CoreVersion:    coreVersionCurrent,
	}
	headerBytes := header.Encode()

	coreEnd := uint64(coreOffset) + uint64(len(core))
	base := align8(coreEnd)

	out := make([]byte, 0, base+1024)
	out = append(out, headerBytes[:]...)
	out = append(out, core...)
	out = appendZeros(out, base-coreEnd)

	commit, err := buildCommit(base, out, 1, 0, sections, key)
	if err != nil {
		return nil, err
	}
	out = append(out, commit...)
	return out, nil
}

type pendingSection struct {
	kind    SectionKind
	payload []byte
}

// buildCommit serializes `sections` as an aligned commit block beginning at
// absolute offset `base`, closing with a CommitFooter. `prefix` is the exact
// image bytes `[0, base)` (header ++ core ++ padding), used to compute the AUTH
// MAC over `[0, auth_header_offset)` when key is non-nil.
func buildCommit(base uint64, prefix []byte, generation, prevGeneration uint64, sections []pendingSection, key *[32]byte) ([]byte, error) {
	out := make([]byte, 0, 256)
	off := base
	var manifestOffset, manifestLen uint64
	var manifestHash [16]byte
	haveManifest := false

	for _, s := range sections {
		pad := align8(off) - off
		out = appendZeros(out, pad)
		off = align8(off)
		headerOffset := off
		payloadOffset := headerOffset + sectionHeaderLen
		checksum := crc32.ChecksumIEEE(s.payload)
		h := SectionHeader{
			Kind:       s.kind,
			Flags:      0,
			Offset:     payloadOffset,
			Length:     uint64(len(s.payload)),
			Generation: generation,
			Checksum:   checksum,
		}
		hb := h.Encode()
		out = append(out, hb[:]...)
		out = append(out, s.payload...)
		off = payloadOffset + uint64(len(s.payload))
		if s.kind == SectionManifest {
			digest := blake3Hash(s.payload)
			manifestOffset = headerOffset
			manifestLen = uint64(len(s.payload))
			copy(manifestHash[:], digest[:16])
			haveManifest = true
		}
	}
	out = appendZeros(out, align8(off)-off)
	off = align8(off)
	if key != nil {
		// The covered range is every byte before the AUTH header: prefix ++ out.
		hasher := blake3.NewHasher()
		hasher.Write(prefix)
		hasher.Write(out)
		covered := hasher.Sum()
		mac := blake3KeyedHash(*key, covered[:])
		h := SectionHeader{
			Kind:       SectionAuth,
			Flags:      0,
			Offset:     off + sectionHeaderLen,
			Length:     32,
			Generation: generation,
			Checksum:   crc32.ChecksumIEEE(mac[:]),
		}
		hb := h.Encode()
		out = append(out, hb[:]...)
		out = append(out, mac[:]...)
		off = h.Offset + 32
		out = appendZeros(out, align8(off)-off)
	}
	if !haveManifest {
		return nil, badSection("commit must include a manifest section")
	}
	footer := CommitFooter{
		Generation:     generation,
		ManifestOffset: manifestOffset,
		ManifestLen:    manifestLen,
		PrevGeneration: prevGeneration,
		ManifestHash:   manifestHash,
	}
	fb := footer.Encode()
	out = append(out, fb[:]...)
	return out, nil
}

func appendZeros(dst []byte, n uint64) []byte {
	for i := uint64(0); i < n; i++ {
		dst = append(dst, 0)
	}
	return dst
}

func toManifestComponents(cs []ComponentReport) []ManifestComponent {
	out := make([]ManifestComponent, len(cs))
	for i, c := range cs {
		out[i] = ManifestComponent{Name: c.Name, Kind: c.Kind, Bytes: c.Bytes}
	}
	return out
}

func deriveObjectID(coreHash [32]byte) [16]byte {
	domain := []byte("ekbloom.object-id.v1")
	buf := make([]byte, 0, len(domain)+32)
	buf = append(buf, domain...)
	buf = append(buf, coreHash[:]...)
	sum := blake3Hash(buf)
	return [16]byte(sum[:16])
}

// buildHistory builds a HistoryFull payload: generation, logical_len, core_len,
// core_hash, count, then count u32 states (x_0 ..= x_N).
func buildHistory(m Machine, core []byte, logicalLen, generation uint64) ([]byte, error) {
	if err := m.CheckCore(core, logicalLen); err != nil {
		return nil, err
	}
	ctx, err := prepare(m, core)
	if err != nil {
		return nil, err
	}
	s, err := m.Init(core)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, 64+int(logicalLen+1)*4)
	out = appendU64(out, generation)
	out = appendU64(out, logicalLen)
	out = appendU64(out, uint64(len(core)))
	coreHash := blake3Hash(core)
	out = append(out, coreHash[:]...)
	out = appendU64(out, logicalLen+1)
	out = appendU32(out, s.State)
	for i := uint64(0); i < logicalLen; i++ {
		if _, err := ctx.reveal(core, &s); err != nil {
			return nil, err
		}
		out = appendU32(out, s.State)
	}
	return out, nil
}

// buildCheckpoints builds a compact checkpoint container (packed machine-1 form).
func buildCheckpoints(m Machine, core []byte, logicalLen uint64, interval uint32) ([]byte, error) {
	if m.ID() != MachineNL2 {
		return nil, &Error{Kind: KindInvalidArgument, Reason: "writer sparse profile supports machine 1 only"}
	}
	if !supportedIntervals[interval] {
		return nil, &Error{Kind: KindBadInterval, Offset: uint64(interval)}
	}
	if err := m.CheckCore(core, logicalLen); err != nil {
		return nil, err
	}
	ctx, err := prepare(m, core)
	if err != nil {
		return nil, err
	}
	s, err := m.Init(core)
	if err != nil {
		return nil, err
	}
	var states []uint32
	states = append(states, s.State)
	for i := uint64(1); i <= logicalLen; i++ {
		if _, err := ctx.reveal(core, &s); err != nil {
			return nil, err
		}
		if i%uint64(interval) == 0 {
			states = append(states, s.State)
		}
	}
	// count = floor(N/K)+1 resume points at positions 0, K, 2K, ...
	count := logicalLen/uint64(interval) + 1
	if uint64(len(states)) < count {
		// ensure the terminal point set matches the Rust count exactly
		for uint64(len(states)) < count {
			states = append(states, s.State)
		}
	}
	out := make([]byte, 0, 22+int(count)*2)
	out = append(out, []byte("EKBCKPT\x00")...)
	out = append(out, 2) // version
	out = append(out, 1) // format: packed
	out = appendU32(out, interval)
	out = appendU64(out, count)
	for j := uint64(0); j < count; j++ {
		out = appendU16(out, uint16(states[j]))
	}
	return out, nil
}
