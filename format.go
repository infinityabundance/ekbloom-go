package ekbloom

import (
	"encoding/binary"
	"hash/crc32"
	"unicode/utf8"
)

// Wire-format constants (see docs/CORE-LAYOUT.md).
var magic = [8]byte{'E', 'K', 'B', 'L', 'O', 'O', 'M', 0}
var commitMarker = [8]byte{'E', 'K', 'B', 'C', 'O', 'M', 'M', 0}

const (
	formatMajor = 0
	formatMinor = 3

	headerLen        = 80
	footerLen        = 64
	sectionHeaderLen = 40
	coreOffset       = headerLen // absolute offset of the visible core

	coreVersionLegacy  = 0
	coreVersionCurrent = 1

	manifestRecordVersion = 1
	authMACLen            = 32
)

// align8 rounds n up to the next multiple of 8 (wrapping, matching Rust).
func align8(n uint64) uint64 { return (n + 7) &^ 7 }

// SectionKind identifies a structure in a `.ekb` file.
type SectionKind uint32

// Section kinds (wire codes).
const (
	SectionCoreMachine      SectionKind = 1
	SectionHistoryFull      SectionKind = 2
	SectionCheckpoints      SectionKind = 3
	SectionBloomGlobal      SectionKind = 4
	SectionBloomContext     SectionKind = 5
	SectionBloomRegion      SectionKind = 6
	SectionBloomPath        SectionKind = 7
	SectionCertificate      SectionKind = 8
	SectionManifest         SectionKind = 9
	SectionAuth             SectionKind = 10
	SectionHistorySegmented SectionKind = 11
	SectionTiles            SectionKind = 12
	SectionBlocks           SectionKind = 13
)

func sectionKindFromU32(v uint32) (SectionKind, bool) {
	switch v {
	case 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13:
		return SectionKind(v), true
	}
	return 0, false
}

// Name is the stable human-readable label used in errors.
func (k SectionKind) Name() string {
	switch k {
	case SectionCoreMachine:
		return "core"
	case SectionHistoryFull:
		return "history"
	case SectionHistorySegmented:
		return "history-segmented"
	case SectionCheckpoints:
		return "checkpoints"
	case SectionBloomGlobal:
		return "bloom-global"
	case SectionBloomContext:
		return "bloom-context"
	case SectionBloomRegion:
		return "bloom-region"
	case SectionBloomPath:
		return "bloom-path"
	case SectionCertificate:
		return "certificate"
	case SectionManifest:
		return "manifest"
	case SectionAuth:
		return "auth"
	case SectionTiles:
		return "tiles"
	case SectionBlocks:
		return "blocks"
	}
	return "unknown"
}

// GenesisHeader is the fixed 80-byte header at offset 0.
type GenesisHeader struct {
	FormatMajor    uint16
	FormatMinor    uint16
	FeatureFlags   uint64
	ObjectID       [16]byte
	LogicalSymbols uint64
	LogicalBytes   uint64
	CoreLen        uint64
	CoreMachineID  uint32
	CoreVersion    uint16
}

// Encode serializes the header to its exact 80-byte layout.
func (h GenesisHeader) Encode() [headerLen]byte {
	var out [headerLen]byte
	copy(out[0:8], magic[:])
	binary.LittleEndian.PutUint16(out[8:], h.FormatMajor)
	binary.LittleEndian.PutUint16(out[10:], h.FormatMinor)
	binary.LittleEndian.PutUint32(out[12:], headerLen)
	binary.LittleEndian.PutUint64(out[16:], h.FeatureFlags)
	copy(out[24:40], h.ObjectID[:])
	binary.LittleEndian.PutUint64(out[40:], h.LogicalSymbols)
	binary.LittleEndian.PutUint64(out[48:], h.LogicalBytes)
	binary.LittleEndian.PutUint64(out[56:], h.CoreLen)
	binary.LittleEndian.PutUint32(out[64:], h.CoreMachineID)
	binary.LittleEndian.PutUint16(out[68:], h.CoreVersion)
	// out[70:80] reserved zero.
	return out
}

// decodeGenesisHeader validates and decodes a header from the start of b.
func decodeGenesisHeader(b []byte) (GenesisHeader, error) {
	var h GenesisHeader
	if len(b) < headerLen {
		return h, truncated(headerLen, uint64(len(b)))
	}
	if [8]byte(b[0:8]) != magic {
		return h, badMagic()
	}
	h.FormatMajor = binary.LittleEndian.Uint16(b[8:])
	h.FormatMinor = binary.LittleEndian.Uint16(b[10:])
	headerLenField := binary.LittleEndian.Uint32(b[12:])
	if headerLenField != headerLen {
		return h, badSection("header_len " + uitoa(uint64(headerLenField)) + " is not 80")
	}
	if h.FormatMajor != formatMajor || h.FormatMinor > formatMinor {
		return h, &Error{Kind: KindUnsupportedVersion, Major: h.FormatMajor, Minor: h.FormatMinor}
	}
	h.FeatureFlags = binary.LittleEndian.Uint64(b[16:])
	if h.FeatureFlags != 0 {
		return h, badSection("unsupported feature_flags")
	}
	copy(h.ObjectID[:], b[24:40])
	h.LogicalSymbols = binary.LittleEndian.Uint64(b[40:])
	h.LogicalBytes = binary.LittleEndian.Uint64(b[48:])
	h.CoreLen = binary.LittleEndian.Uint64(b[56:])
	h.CoreMachineID = binary.LittleEndian.Uint32(b[64:])
	h.CoreVersion = binary.LittleEndian.Uint16(b[68:])
	if _, known := machineByID(h.CoreMachineID); known {
		if !coreVersionSupported(h.CoreMachineID, h.CoreVersion) {
			return h, &Error{Kind: KindUnsupportedCoreVersion, MachineID: h.CoreMachineID, CoreVersion: h.CoreVersion}
		}
	}
	return h, nil
}

func coreVersionSupported(machineID uint32, v uint16) bool {
	_, known := machineByID(machineID)
	if !known {
		return false
	}
	return v == coreVersionLegacy || v == coreVersionCurrent
}

// SectionHeader is the fixed 40-byte header preceding a section payload.
type SectionHeader struct {
	Kind       SectionKind
	Flags      uint32
	Offset     uint64
	Length     uint64
	Generation uint64
	Checksum   uint32
}

// Encode serializes the section header to its exact 40-byte layout.
func (s SectionHeader) Encode() [sectionHeaderLen]byte {
	var out [sectionHeaderLen]byte
	binary.LittleEndian.PutUint32(out[0:], uint32(s.Kind))
	binary.LittleEndian.PutUint32(out[4:], s.Flags)
	binary.LittleEndian.PutUint64(out[8:], s.Offset)
	binary.LittleEndian.PutUint64(out[16:], s.Length)
	binary.LittleEndian.PutUint64(out[24:], s.Generation)
	binary.LittleEndian.PutUint32(out[32:], s.Checksum)
	return out
}

func decodeSectionHeader(b []byte) (SectionHeader, error) {
	var s SectionHeader
	if len(b) < sectionHeaderLen {
		return s, truncated(sectionHeaderLen, uint64(len(b)))
	}
	kind, ok := sectionKindFromU32(binary.LittleEndian.Uint32(b[0:]))
	if !ok {
		return s, badSection("unknown section kind")
	}
	s.Kind = kind
	s.Flags = binary.LittleEndian.Uint32(b[4:])
	s.Offset = binary.LittleEndian.Uint64(b[8:])
	s.Length = binary.LittleEndian.Uint64(b[16:])
	s.Generation = binary.LittleEndian.Uint64(b[24:])
	s.Checksum = binary.LittleEndian.Uint32(b[32:])
	return s, nil
}

// CommitFooter is the fixed 64-byte footer closing a commit block.
type CommitFooter struct {
	Generation     uint64
	ManifestOffset uint64
	ManifestLen    uint64
	PrevGeneration uint64
	ManifestHash   [16]byte
}

func footerChecksum(footer []byte) uint32 {
	var buf [footerLen - 4]byte
	copy(buf[0:56], footer[0:56])
	copy(buf[56:], footer[60:footerLen])
	return crc32.ChecksumIEEE(buf[:])
}

// Encode serializes the footer to its exact 64-byte layout.
func (f CommitFooter) Encode() [footerLen]byte {
	var out [footerLen]byte
	binary.LittleEndian.PutUint64(out[0:], f.Generation)
	binary.LittleEndian.PutUint64(out[8:], f.ManifestOffset)
	binary.LittleEndian.PutUint64(out[16:], f.ManifestLen)
	binary.LittleEndian.PutUint64(out[24:], f.PrevGeneration)
	copy(out[32:48], f.ManifestHash[:])
	copy(out[48:56], commitMarker[:])
	// out[56:60] checksum placeholder, out[60:64] reserved
	sum := footerChecksum(out[:])
	binary.LittleEndian.PutUint32(out[56:], sum)
	return out
}

func decodeCommitFooter(b []byte) (CommitFooter, error) {
	var f CommitFooter
	if len(b) < footerLen {
		return f, truncated(footerLen, uint64(len(b)))
	}
	f.Generation = binary.LittleEndian.Uint64(b[0:])
	f.ManifestOffset = binary.LittleEndian.Uint64(b[8:])
	f.ManifestLen = binary.LittleEndian.Uint64(b[16:])
	f.PrevGeneration = binary.LittleEndian.Uint64(b[24:])
	copy(f.ManifestHash[:], b[32:48])
	if [8]byte(b[48:56]) != commitMarker {
		return f, badSection("commit footer marker missing")
	}
	if binary.LittleEndian.Uint32(b[56:]) != footerChecksum(b[:footerLen]) {
		return f, &Error{Kind: KindChecksumMismatch, Section: "commit-footer"}
	}
	return f, nil
}

// ManifestComponent is one accounting entry in a manifest record.
type ManifestComponent struct {
	Name  string
	Kind  string
	Bytes uint64
}

// ManifestRecord is the self-describing record stored in a MANIFEST section.
type ManifestRecord struct {
	Version    uint32
	Generation uint64
	LogicalLen uint64
	ObjectID   [16]byte
	MachineID  uint32
	CoreHash   [32]byte
	Profiles   []string
	Components []ManifestComponent
}

// encode serializes the manifest record.
func (m ManifestRecord) encode() []byte {
	out := make([]byte, 0, 128)
	out = appendU32(out, manifestRecordVersion)
	out = appendU64(out, m.Generation)
	out = appendU64(out, m.LogicalLen)
	out = append(out, m.ObjectID[:]...)
	out = appendU32(out, m.MachineID)
	out = append(out, m.CoreHash[:]...)
	out = appendU32(out, uint32(len(m.Profiles)))
	for _, p := range m.Profiles {
		out = appendU32(out, uint32(len(p)))
		out = append(out, p...)
	}
	out = appendU32(out, uint32(len(m.Components)))
	for _, c := range m.Components {
		out = appendU32(out, uint32(len(c.Name)))
		out = append(out, c.Name...)
		out = appendU32(out, uint32(len(c.Kind)))
		out = append(out, c.Kind...)
		out = appendU64(out, c.Bytes)
	}
	return out
}

func decodeManifestRecord(b []byte) (ManifestRecord, error) {
	var m ManifestRecord
	r := newLE(b)
	ver, err := r.u32()
	if err != nil {
		return m, err
	}
	if ver != manifestRecordVersion {
		return m, &Error{Kind: KindUnsupportedManifestVersion, Version: ver}
	}
	if m.Generation, err = r.u64(); err != nil {
		return m, err
	}
	if m.LogicalLen, err = r.u64(); err != nil {
		return m, err
	}
	if err = r.arr16(&m.ObjectID); err != nil {
		return m, err
	}
	if m.MachineID, err = r.u32(); err != nil {
		return m, err
	}
	if err = r.arr32(&m.CoreHash); err != nil {
		return m, err
	}
	pc, err := r.u32()
	if err != nil {
		return m, err
	}
	m.Profiles = make([]string, 0, min(int(pc), 1<<16))
	for i := uint32(0); i < pc; i++ {
		n, err := r.u32()
		if err != nil {
			return m, err
		}
		s, err := r.utf8(n)
		if err != nil {
			return m, err
		}
		m.Profiles = append(m.Profiles, s)
	}
	cc, err := r.u32()
	if err != nil {
		return m, err
	}
	m.Components = make([]ManifestComponent, 0, min(int(cc), 1<<16))
	for i := uint32(0); i < cc; i++ {
		nameLen, err := r.u32()
		if err != nil {
			return m, err
		}
		name, err := r.utf8(nameLen)
		if err != nil {
			return m, err
		}
		kindLen, err := r.u32()
		if err != nil {
			return m, err
		}
		kind, err := r.utf8(kindLen)
		if err != nil {
			return m, err
		}
		bytes, err := r.u64()
		if err != nil {
			return m, err
		}
		m.Components = append(m.Components, ManifestComponent{Name: name, Kind: kind, Bytes: bytes})
	}
	if r.remaining() != 0 {
		return m, badSection("trailing bytes after manifest record")
	}
	return m, nil
}

// ScannedSection describes one section discovered by Scan.
type ScannedSection struct {
	Kind          SectionKind
	Generation    uint64
	HeaderOffset  uint64
	PayloadOffset uint64
	PayloadLen    uint64
}

// ScannedCommit is a fully validated commit block.
type ScannedCommit struct {
	Generation     uint64
	PrevGeneration uint64
	ManifestOffset uint64
	ManifestLen    uint64
	ManifestHash   [16]byte
	Start          uint64
	End            uint64
	Sections       []ScannedSection
}

// ScanOutcome is the result of a forward scan of a `.ekb` file.
type ScanOutcome struct {
	Header        GenesisHeader
	CoreStart     uint64
	CoreEnd       uint64
	Commits       []ScannedCommit
	TornTailBytes uint64
	TornNote      string
	tornErr       error
}

// Scan forward-scans a `.ekb` file, returning the valid commit prefix and any
// unvalidated tail. Behavior matches the Rust `format::scan` exactly: parsing
// stops at the first structural failure, so Commits is the valid prefix.
func Scan(b []byte) (ScanOutcome, error) {
	var out ScanOutcome
	header, err := decodeGenesisHeader(b)
	if err != nil {
		return out, err
	}
	out.Header = header
	coreEnd := coreOffset + header.CoreLen
	if coreEnd < coreOffset {
		return out, overflow("core extent")
	}
	if uint64(len(b)) < coreEnd {
		return out, truncated(coreEnd, uint64(len(b)))
	}
	out.CoreStart = coreOffset
	out.CoreEnd = coreEnd

	total := uint64(len(b))
	pos := align8(coreEnd)
	var prev uint64
	for pos < total {
		commit, err := parseCommit(b, pos, prev)
		if err != nil {
			out.TornNote = err.Error()
			out.tornErr = err
			break
		}
		prev = commit.Generation
		pos = commit.End
		out.Commits = append(out.Commits, commit)
	}
	if total >= pos {
		out.TornTailBytes = total - pos
	}
	return out, nil
}

func parseCommit(b []byte, start, expectedPrev uint64) (ScannedCommit, error) {
	var commit ScannedCommit
	total := uint64(len(b))
	cur := start
	var footerStart uint64
	var sections []ScannedSection

	for {
		headerEnd := cur + sectionHeaderLen
		if headerEnd < cur {
			return commit, overflow("section header extent")
		}
		if total < headerEnd {
			return commit, truncated(headerEnd, total)
		}
		header, err := decodeSectionHeader(b[cur:headerEnd])
		if err != nil {
			return commit, err
		}
		if header.Offset != headerEnd {
			return commit, badSection("section offset does not follow its header")
		}
		payloadEnd := headerEnd + header.Length
		if payloadEnd < headerEnd {
			return commit, overflow("section payload extent")
		}
		if total < payloadEnd {
			return commit, truncated(payloadEnd, total)
		}
		payload := b[headerEnd:payloadEnd]
		if crc32.ChecksumIEEE(payload) != header.Checksum {
			return commit, &Error{Kind: KindChecksumMismatch, Section: header.Kind.Name()}
		}
		sections = append(sections, ScannedSection{
			Kind:          header.Kind,
			Generation:    header.Generation,
			HeaderOffset:  cur,
			PayloadOffset: headerEnd,
			PayloadLen:    header.Length,
		})
		cur = align8(payloadEnd)
		if header.Kind == SectionManifest {
			if auth, next, ok := scanAuthOpt(b, cur, total); ok {
				sections = append(sections, auth)
				cur = next
			}
			footerStart = cur
			break
		}
	}

	footerEnd := footerStart + footerLen
	if footerEnd < footerStart {
		return commit, overflow("footer extent")
	}
	if total < footerEnd {
		return commit, truncated(footerEnd, total)
	}
	footer, err := decodeCommitFooter(b[footerStart:footerEnd])
	if err != nil {
		return commit, err
	}

	manifestIdx := -1
	for i := len(sections) - 1; i >= 0; i-- {
		if sections[i].Kind == SectionManifest {
			manifestIdx = i
			break
		}
	}
	if manifestIdx < 0 {
		return commit, badSection("commit has no manifest section")
	}
	for _, s := range sections[manifestIdx+1:] {
		if s.Kind != SectionAuth {
			return commit, badSection("commit does not end with its manifest (and optional AUTH)")
		}
	}
	manifest := sections[manifestIdx]
	if footer.ManifestOffset != manifest.HeaderOffset || footer.ManifestLen != manifest.PayloadLen {
		return commit, badSection("footer manifest pointer disagrees with section")
	}
	if footer.PrevGeneration != expectedPrev {
		return commit, badSection("footer prev_generation does not link to predecessor")
	}
	manifestPayload := b[manifest.PayloadOffset : manifest.PayloadOffset+manifest.PayloadLen]
	h := blake3Hash(manifestPayload)
	if footer.ManifestHash != [16]byte(h[:16]) {
		return commit, &Error{Kind: KindChecksumMismatch, Section: "manifest"}
	}
	record, err := decodeManifestRecord(manifestPayload)
	if err != nil {
		return commit, err
	}
	if record.Generation != footer.Generation {
		return commit, badSection("manifest generation disagrees with footer")
	}

	commit = ScannedCommit{
		Generation:     footer.Generation,
		PrevGeneration: footer.PrevGeneration,
		ManifestOffset: footer.ManifestOffset,
		ManifestLen:    footer.ManifestLen,
		ManifestHash:   footer.ManifestHash,
		Start:          start,
		End:            footerEnd,
		Sections:       sections,
	}
	return commit, nil
}

func scanAuthOpt(b []byte, offset, total uint64) (ScannedSection, uint64, bool) {
	headerEnd := offset + sectionHeaderLen
	if headerEnd < offset || headerEnd > total {
		return ScannedSection{}, 0, false
	}
	header, err := decodeSectionHeader(b[offset:headerEnd])
	if err != nil || header.Kind != SectionAuth || header.Offset != headerEnd || header.Length != authMACLen {
		return ScannedSection{}, 0, false
	}
	payloadEnd := headerEnd + header.Length
	if payloadEnd < headerEnd || payloadEnd > total {
		return ScannedSection{}, 0, false
	}
	if crc32.ChecksumIEEE(b[headerEnd:payloadEnd]) != header.Checksum {
		return ScannedSection{}, 0, false
	}
	return ScannedSection{
		Kind:          SectionAuth,
		Generation:    header.Generation,
		HeaderOffset:  offset,
		PayloadOffset: headerEnd,
		PayloadLen:    header.Length,
	}, align8(payloadEnd), true
}

// CommitManifest decodes the manifest record of a scanned commit.
func CommitManifest(b []byte, commit *ScannedCommit) (ManifestRecord, error) {
	for i := len(commit.Sections) - 1; i >= 0; i-- {
		s := commit.Sections[i]
		if s.Kind == SectionManifest {
			return decodeManifestRecord(b[s.PayloadOffset : s.PayloadOffset+s.PayloadLen])
		}
	}
	return ManifestRecord{}, badSection("commit has no manifest section")
}

// sectionPayload returns a section's payload bytes.
func sectionPayload(b []byte, s ScannedSection) []byte {
	return b[s.PayloadOffset : s.PayloadOffset+s.PayloadLen]
}

// ---- small helpers --------------------------------------------------------

func appendU32(dst []byte, v uint32) []byte {
	var t [4]byte
	binary.LittleEndian.PutUint32(t[:], v)
	return append(dst, t[:]...)
}
func appendU64(dst []byte, v uint64) []byte {
	var t [8]byte
	binary.LittleEndian.PutUint64(t[:], v)
	return append(dst, t[:]...)
}

// leReader is a bounds-checked little-endian cursor, matching Rust's Reader.
type leReader struct {
	b   []byte
	off int
}

func newLE(b []byte) *leReader { return &leReader{b: b} }

func (r *leReader) remaining() int { return len(r.b) - r.off }

func (r *leReader) take(n uint64) ([]byte, error) {
	end := uint64(r.off) + n
	if end < uint64(r.off) || end > uint64(len(r.b)) {
		return nil, truncated(end, uint64(len(r.b)))
	}
	out := r.b[r.off:end]
	r.off = int(end)
	return out, nil
}

func (r *leReader) u8() (uint8, error) {
	if r.off+1 > len(r.b) {
		return 0, truncated(uint64(r.off+1), uint64(len(r.b)))
	}
	v := r.b[r.off]
	r.off++
	return v, nil
}
func (r *leReader) u16() (uint16, error) {
	if r.off+2 > len(r.b) {
		return 0, truncated(uint64(r.off+2), uint64(len(r.b)))
	}
	v := binary.LittleEndian.Uint16(r.b[r.off:])
	r.off += 2
	return v, nil
}
func (r *leReader) u32() (uint32, error) {
	if r.off+4 > len(r.b) {
		return 0, truncated(uint64(r.off+4), uint64(len(r.b)))
	}
	v := binary.LittleEndian.Uint32(r.b[r.off:])
	r.off += 4
	return v, nil
}
func (r *leReader) u64() (uint64, error) {
	if r.off+8 > len(r.b) {
		return 0, truncated(uint64(r.off+8), uint64(len(r.b)))
	}
	v := binary.LittleEndian.Uint64(r.b[r.off:])
	r.off += 8
	return v, nil
}
func (r *leReader) arr16(dst *[16]byte) error {
	if r.off+16 > len(r.b) {
		return truncated(uint64(r.off+16), uint64(len(r.b)))
	}
	copy(dst[:], r.b[r.off:])
	r.off += 16
	return nil
}
func (r *leReader) arr32(dst *[32]byte) error {
	if r.off+32 > len(r.b) {
		return truncated(uint64(r.off+32), uint64(len(r.b)))
	}
	copy(dst[:], r.b[r.off:])
	r.off += 32
	return nil
}
func (r *leReader) utf8(n uint32) (string, error) {
	raw, err := r.take(uint64(n))
	if err != nil {
		return "", err
	}
	if !utf8.Valid(raw) {
		return "", badSection("invalid UTF-8 in string")
	}
	return string(raw), nil
}

func uitoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
