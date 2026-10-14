package ekb

import (
	"encoding/binary"
	"hash/crc32"
	"io"
	"sort"

	"github.com/infinityabundance/ekbloom-go/internal/blake3"
)

// Streaming container constants (mirrors the Rust `stream` module).
var streamMagic = [8]byte{'E', 'K', 'S', 'T', 'R', 'E', 'A', 'M'}
var streamFooterMagic = [8]byte{'E', 'K', 'S', 'F', 'T', 'R', 'M', 0}

const (
	streamVersion           = 1
	streamHeaderBytes       = 64
	streamEntryBytes        = 56
	streamFooterBytes       = 160
	StreamDefaultBlockBytes = 1 << 16
)

// Header field offsets.
const (
	sHMagic      = 0
	sHVersion    = 8
	sHHeaderLen  = 10
	sHFlags      = 12
	sHBlockBytes = 16
	sHPolicy     = 20
	sHCRC        = 52
)

// Footer field offsets.
const (
	sFMagic           = 0
	sFVersion         = 8
	sFFooterLen       = 10
	sFFlags           = 12
	sFBlockBytes      = 16
	sFPolicy          = 20
	sFBlockCount      = 24
	sFLogicalLen      = 32
	sFDirectoryOffset = 40
	sFDirectoryLen    = 48
	sFPayloadOffset   = 56
	sFPayloadBytes    = 64
	sFSourceHash      = 72
	sFDirectoryHash   = 104
	sFHeaderCRC       = 136
	sFCRC             = 152
)

// Stream entry field offsets.
const (
	sELogicalStart   = 0
	sELogicalLen     = 8
	sEPhysicalOffset = 16
	sEPhysicalLen    = 24
	sEMachineID      = 32
	sEModelRef       = 36
	sEContentID      = 40
)

// Policy selects the per-block representation of a stream.
type Policy uint32

// Representation policies.
const (
	PolicyAuto Policy = iota
	PolicyPacked
	PolicySegmented
	PolicyWord16
	PolicyIndependentLanes
)

func policyFromCode(c uint32) (Policy, bool) {
	if c <= uint32(PolicyIndependentLanes) {
		return Policy(c), true
	}
	return 0, false
}

// StreamOptions controls a StreamBuilder.
type StreamOptions struct {
	// BlockBytes is the logical block span (the builder's memory bound).
	BlockBytes uint32
	// Machine is the representation policy applied per block.
	Machine Policy
}

// DefaultStreamOptions returns the reference defaults.
func DefaultStreamOptions() StreamOptions {
	return StreamOptions{BlockBytes: StreamDefaultBlockBytes, Machine: PolicyAuto}
}

// StreamManifest summarizes a finished stream container.
type StreamManifest struct {
	LogicalLen     uint64
	BlockBytes     uint32
	BlockCount     uint64
	Policy         Policy
	ContainerBytes uint64
	PayloadBytes   uint64
	DirectoryBytes uint64
	HeaderBytes    uint64
	FooterBytes    uint64
	SourceHash     [32]byte
	DirectoryHash  [32]byte
	Machines       []MachineCount
}

// MachineCount is a per-machine block count.
type MachineCount struct {
	MachineID uint32
	Blocks    uint64
}

// StreamEntry is one block directory entry.
type StreamEntry struct {
	LogicalStart   uint64
	LogicalLen     uint64
	PhysicalOffset uint64
	PhysicalLen    uint64
	MachineID      uint32
	ModelRef       uint32
	ContentID      [16]byte
}

func streamContentID(b []byte) [16]byte {
	h := blake3Hash(b)
	return [16]byte(h[:16])
}

func streamEncodeEntry(dst []byte, e StreamEntry) {
	binary.LittleEndian.PutUint64(dst[sELogicalStart:], e.LogicalStart)
	binary.LittleEndian.PutUint64(dst[sELogicalLen:], e.LogicalLen)
	binary.LittleEndian.PutUint64(dst[sEPhysicalOffset:], e.PhysicalOffset)
	binary.LittleEndian.PutUint64(dst[sEPhysicalLen:], e.PhysicalLen)
	binary.LittleEndian.PutUint32(dst[sEMachineID:], e.MachineID)
	binary.LittleEndian.PutUint32(dst[sEModelRef:], e.ModelRef)
	copy(dst[sEContentID:sEContentID+16], e.ContentID[:])
}

func streamDecodeEntry(b []byte) StreamEntry {
	var c [16]byte
	copy(c[:], b[sEContentID:sEContentID+16])
	return StreamEntry{
		LogicalStart:   binary.LittleEndian.Uint64(b[sELogicalStart:]),
		LogicalLen:     binary.LittleEndian.Uint64(b[sELogicalLen:]),
		PhysicalOffset: binary.LittleEndian.Uint64(b[sEPhysicalOffset:]),
		PhysicalLen:    binary.LittleEndian.Uint64(b[sEPhysicalLen:]),
		MachineID:      binary.LittleEndian.Uint32(b[sEMachineID:]),
		ModelRef:       binary.LittleEndian.Uint32(b[sEModelRef:]),
		ContentID:      c,
	}
}

// streamEncodeBlock chooses a representation for one block.
func streamEncodeBlock(block []byte, policy Policy) (uint32, []byte, error) {
	prefer := func(id uint32) (uint32, []byte, error) {
		m, ok := machineByID(id)
		if !ok {
			return 0, nil, &Error{Kind: KindUnknownMachine, MachineID: id}
		}
		core, err := m.Encode(block)
		if err != nil {
			return 0, nil, err
		}
		if len(core) < len(block) {
			return id, core, nil
		}
		cp := make([]byte, len(block))
		copy(cp, block)
		return MachineStore, cp, nil
	}
	switch policy {
	case PolicyAuto:
		bestID := MachineStore
		best := append([]byte(nil), block...)
		for _, id := range []uint32{MachinePack, MachineSeg} {
			m, _ := machineByID(id)
			core, err := m.Encode(block)
			if err != nil {
				return 0, nil, err
			}
			if len(core) < len(best) {
				bestID = id
				best = core
			}
		}
		return bestID, best, nil
	case PolicyPacked:
		return prefer(MachinePack)
	case PolicySegmented:
		return prefer(MachineSeg)
	case PolicyWord16:
		return prefer(MachineRans0W)
	case PolicyIndependentLanes:
		return prefer(MachineRans0IL)
	}
	return prefer(MachineStore)
}

// StreamBuilder writes a downstream-seekable stream container while holding at
// most one block plus an O(blocks) directory (bounded RSS). Write to any
// io.Writer; Finish writes the directory and footer.
type StreamBuilder struct {
	sink       io.Writer
	blockBytes uint32
	policy     Policy
	buf        []byte
	entries    []StreamEntry
	header     [streamHeaderBytes]byte
	pos        uint64
	sinkOffset uint64
	hasher     *blake3.Hasher
}

// NewStreamBuilder starts a container, writing the header to sink.
func NewStreamBuilder(sink io.Writer, opts StreamOptions) (*StreamBuilder, error) {
	if opts.BlockBytes == 0 {
		return nil, &Error{Kind: KindInvalidArgument, Reason: "stream block_bytes must be non-zero"}
	}
	b := &StreamBuilder{
		sink:       sink,
		blockBytes: opts.BlockBytes,
		policy:     opts.Machine,
		hasher:     blake3.NewHasher(),
	}
	copy(b.header[sHMagic:sHMagic+8], streamMagic[:])
	binary.LittleEndian.PutUint16(b.header[sHVersion:], streamVersion)
	binary.LittleEndian.PutUint16(b.header[sHHeaderLen:], streamHeaderBytes)
	binary.LittleEndian.PutUint32(b.header[sHFlags:], 0)
	binary.LittleEndian.PutUint32(b.header[sHBlockBytes:], opts.BlockBytes)
	binary.LittleEndian.PutUint32(b.header[sHPolicy:], uint32(opts.Machine))
	binary.LittleEndian.PutUint32(b.header[sHCRC:], crc32IEEE(b.header[:sHCRC]))
	if _, err := sink.Write(b.header[:]); err != nil {
		return nil, &Error{Kind: KindBadSection, Reason: "stream header write: " + err.Error()}
	}
	b.sinkOffset = streamHeaderBytes
	return b, nil
}

// Write appends logical bytes, flushing a block whenever the buffer is full.
func (b *StreamBuilder) Write(data []byte) error {
	b.hasher.Write(data)
	rest := data
	for len(rest) > 0 {
		space := int(b.blockBytes) - len(b.buf)
		take := space
		if take > len(rest) {
			take = len(rest)
		}
		b.buf = append(b.buf, rest[:take]...)
		rest = rest[take:]
		if len(b.buf) == int(b.blockBytes) {
			if err := b.flushBlock(); err != nil {
				return err
			}
		}
	}
	return nil
}

// Checkpoint ends the current block at the current position.
func (b *StreamBuilder) Checkpoint() error { return b.flushBlock() }

// Finish flushes the final block, writes the directory and footer, and returns
// the summary manifest.
func (b *StreamBuilder) Finish() (StreamManifest, error) {
	var m StreamManifest
	if err := b.flushBlock(); err != nil {
		return m, err
	}
	directoryOffset := b.sinkOffset
	dirHasher := blake3.NewHasher()
	entry := make([]byte, streamEntryBytes)
	for _, e := range b.entries {
		streamEncodeEntry(entry, e)
		if _, err := b.sink.Write(entry); err != nil {
			return m, &Error{Kind: KindBadSection, Reason: "stream directory write: " + err.Error()}
		}
		dirHasher.Write(entry)
	}
	directoryBytes := uint64(len(b.entries)) * streamEntryBytes
	directoryHash := dirHasher.Sum()
	payloadOffset := uint64(streamHeaderBytes)
	payloadBytes := directoryOffset - payloadOffset
	sourceHash := b.hasher.Sum()

	counts := map[uint32]uint64{}
	for _, e := range b.entries {
		counts[e.MachineID]++
	}
	var machines []MachineCount
	for id, n := range counts {
		machines = append(machines, MachineCount{MachineID: id, Blocks: n})
	}
	sort.Slice(machines, func(i, j int) bool { return machines[i].MachineID < machines[j].MachineID })

	var footer [streamFooterBytes]byte
	copy(footer[sFMagic:sFMagic+8], streamFooterMagic[:])
	binary.LittleEndian.PutUint16(footer[sFVersion:], streamVersion)
	binary.LittleEndian.PutUint16(footer[sFFooterLen:], streamFooterBytes)
	binary.LittleEndian.PutUint32(footer[sFFlags:], 0)
	binary.LittleEndian.PutUint32(footer[sFBlockBytes:], b.blockBytes)
	binary.LittleEndian.PutUint32(footer[sFPolicy:], uint32(b.policy))
	binary.LittleEndian.PutUint64(footer[sFBlockCount:], uint64(len(b.entries)))
	binary.LittleEndian.PutUint64(footer[sFLogicalLen:], b.pos)
	binary.LittleEndian.PutUint64(footer[sFDirectoryOffset:], directoryOffset)
	binary.LittleEndian.PutUint64(footer[sFDirectoryLen:], directoryBytes)
	binary.LittleEndian.PutUint64(footer[sFPayloadOffset:], payloadOffset)
	binary.LittleEndian.PutUint64(footer[sFPayloadBytes:], payloadBytes)
	copy(footer[sFSourceHash:sFSourceHash+32], sourceHash[:])
	copy(footer[sFDirectoryHash:sFDirectoryHash+32], directoryHash[:])
	binary.LittleEndian.PutUint32(footer[sFHeaderCRC:], binary.LittleEndian.Uint32(b.header[sHCRC:]))
	binary.LittleEndian.PutUint32(footer[sFCRC:], crc32IEEE(footer[:sFCRC]))
	if _, err := b.sink.Write(footer[:]); err != nil {
		return m, &Error{Kind: KindBadSection, Reason: "stream footer write: " + err.Error()}
	}

	m = StreamManifest{
		LogicalLen:     b.pos,
		BlockBytes:     b.blockBytes,
		BlockCount:     uint64(len(b.entries)),
		Policy:         b.policy,
		ContainerBytes: directoryOffset + directoryBytes + streamFooterBytes,
		PayloadBytes:   payloadBytes,
		DirectoryBytes: directoryBytes,
		HeaderBytes:    streamHeaderBytes,
		FooterBytes:    streamFooterBytes,
		SourceHash:     sourceHash,
		DirectoryHash:  directoryHash,
		Machines:       machines,
	}
	return m, nil
}

func (b *StreamBuilder) flushBlock() error {
	if len(b.buf) == 0 {
		return nil
	}
	logicalLen := uint64(len(b.buf))
	id, payload, err := streamEncodeBlock(b.buf, b.policy)
	if err != nil {
		return err
	}
	cid := streamContentID(payload)
	physicalOffset := b.sinkOffset
	if _, err := b.sink.Write(payload); err != nil {
		return &Error{Kind: KindBadSection, Reason: "stream block write: " + err.Error()}
	}
	b.entries = append(b.entries, StreamEntry{
		LogicalStart:   b.pos,
		LogicalLen:     logicalLen,
		PhysicalOffset: physicalOffset,
		PhysicalLen:    uint64(len(payload)),
		MachineID:      id,
		ModelRef:       0,
		ContentID:      cid,
	})
	b.pos += logicalLen
	b.sinkOffset += uint64(len(payload))
	b.buf = b.buf[:0]
	return nil
}

// StreamReader is a validated, read-only view over an in-memory stream container.
type StreamReader struct {
	data           []byte
	logicalLen     uint64
	blockBytes     uint32
	policy         Policy
	uniform        bool
	entries        []StreamEntry
	payloadOffset  uint64
	payloadBytes   uint64
	directoryBytes uint64
	containerBytes uint64
	sourceHash     [32]byte
	directoryHash  [32]byte
	seqPos         uint64
}

// OpenStream validates and opens an in-memory stream container.
func OpenStream(data []byte) (*StreamReader, error) {
	if uint64(len(data)) < streamHeaderBytes+streamFooterBytes {
		return nil, truncated(streamHeaderBytes+streamFooterBytes, uint64(len(data)))
	}
	header := data[:streamHeaderBytes]
	if [8]byte(header[sHMagic:sHMagic+8]) != streamMagic {
		return nil, badMagic()
	}
	if binary.LittleEndian.Uint16(header[sHVersion:]) != streamVersion {
		return nil, &Error{Kind: KindUnsupportedVersion, Major: streamVersion}
	}
	if binary.LittleEndian.Uint16(header[sHHeaderLen:]) != streamHeaderBytes {
		return nil, badSection("stream: header length is not 64")
	}
	if binary.LittleEndian.Uint32(header[sHFlags:]) != 0 {
		return nil, badSection("stream: unknown header flags")
	}
	blockBytes := binary.LittleEndian.Uint32(header[sHBlockBytes:])
	if blockBytes == 0 {
		return nil, badSection("stream: header block_bytes is zero")
	}
	policy, ok := policyFromCode(binary.LittleEndian.Uint32(header[sHPolicy:]))
	if !ok {
		return nil, badSection("stream: unknown representation policy")
	}
	headerCRC := binary.LittleEndian.Uint32(header[sHCRC:])
	if crc32IEEE(header[:sHCRC]) != headerCRC {
		return nil, &Error{Kind: KindChecksumMismatch, Section: "stream header"}
	}

	total := uint64(len(data))
	footerOffset := total - streamFooterBytes
	footer := data[footerOffset:]
	if [8]byte(footer[sFMagic:sFMagic+8]) != streamFooterMagic {
		return nil, badSection("stream: bad footer magic")
	}
	if binary.LittleEndian.Uint16(footer[sFVersion:]) != streamVersion ||
		binary.LittleEndian.Uint16(footer[sFFooterLen:]) != streamFooterBytes ||
		binary.LittleEndian.Uint32(footer[sFFlags:]) != 0 {
		return nil, badSection("stream: footer version/flags")
	}
	if binary.LittleEndian.Uint32(footer[sFBlockBytes:]) != blockBytes ||
		binary.LittleEndian.Uint32(footer[sFPolicy:]) != uint32(policy) ||
		binary.LittleEndian.Uint32(footer[sFHeaderCRC:]) != headerCRC {
		return nil, badSection("stream: footer disagrees with the header")
	}
	if crc32IEEE(footer[:sFCRC]) != binary.LittleEndian.Uint32(footer[sFCRC:]) {
		return nil, &Error{Kind: KindChecksumMismatch, Section: "stream footer"}
	}

	blockCount := binary.LittleEndian.Uint64(footer[sFBlockCount:])
	logicalLen := binary.LittleEndian.Uint64(footer[sFLogicalLen:])
	directoryOffset := binary.LittleEndian.Uint64(footer[sFDirectoryOffset:])
	directoryLen := binary.LittleEndian.Uint64(footer[sFDirectoryLen:])
	payloadOffset := binary.LittleEndian.Uint64(footer[sFPayloadOffset:])
	payloadBytes := binary.LittleEndian.Uint64(footer[sFPayloadBytes:])
	var sourceHash, directoryHash [32]byte
	copy(sourceHash[:], footer[sFSourceHash:])
	copy(directoryHash[:], footer[sFDirectoryHash:])

	if payloadOffset != streamHeaderBytes {
		return nil, badSection("stream: payload offset is not the header length")
	}
	if directoryLen != blockCount*streamEntryBytes {
		return nil, badSection("stream: directory length disagrees with block count")
	}
	if payloadOffset+payloadBytes != directoryOffset || directoryOffset+directoryLen != footerOffset {
		return nil, badSection("stream: directory/payload/footer geometry is inconsistent")
	}
	if err := checkDefaultAllocation(directoryLen); err != nil {
		return nil, err
	}
	dirBytes := data[directoryOffset : directoryOffset+directoryLen]
	if blake3Hash(dirBytes) != directoryHash {
		return nil, &Error{Kind: KindChecksumMismatch, Section: "stream directory"}
	}

	r := &StreamReader{
		data:           data,
		logicalLen:     logicalLen,
		blockBytes:     blockBytes,
		policy:         policy,
		uniform:        true,
		payloadOffset:  payloadOffset,
		payloadBytes:   payloadBytes,
		directoryBytes: directoryLen,
		containerBytes: total,
		sourceHash:     sourceHash,
		directoryHash:  directoryHash,
	}
	var logicalCursor, physCursor = uint64(0), payloadOffset
	for i := uint64(0); i < blockCount; i++ {
		o := i * streamEntryBytes
		e := streamDecodeEntry(dirBytes[o : o+streamEntryBytes])
		if e.LogicalStart != logicalCursor {
			return nil, badSection("stream: block logical_start is not contiguous")
		}
		if e.LogicalLen == 0 || e.LogicalLen > uint64(blockBytes) {
			return nil, badSection("stream: block has invalid logical length")
		}
		if e.PhysicalLen == 0 {
			return nil, badSection("stream: block has an empty payload")
		}
		if e.PhysicalOffset != physCursor {
			return nil, badSection("stream: block payload is not contiguous")
		}
		if e.ModelRef != 0 {
			return nil, badSection("stream: block has a model reference")
		}
		if e.MachineID != MachineStore {
			if _, ok := machineByID(e.MachineID); !ok {
				return nil, &Error{Kind: KindUnknownMachine, MachineID: e.MachineID}
			}
		}
		if i+1 < blockCount && e.LogicalLen != uint64(blockBytes) {
			r.uniform = false
		}
		logicalCursor += e.LogicalLen
		physCursor += e.PhysicalLen
		r.entries = append(r.entries, e)
	}
	if logicalCursor != logicalLen {
		return nil, badSection("stream: blocks do not cover the declared logical length")
	}
	if physCursor != directoryOffset {
		return nil, badSection("stream: block payloads do not reach the directory")
	}
	return r, nil
}

// Len is the logical byte length.
func (r *StreamReader) Len() uint64   { return r.logicalLen }
func (r *StreamReader) IsEmpty() bool { return r.logicalLen == 0 }

// BlockBytes is the logical block span.
func (r *StreamReader) BlockBytes() uint32 { return r.blockBytes }

// Policy is the representation policy.
func (r *StreamReader) Policy() Policy { return r.policy }

// BlockCount is the number of blocks.
func (r *StreamReader) BlockCount() uint64 { return uint64(len(r.entries)) }

// ContainerBytes is the total container size.
func (r *StreamReader) ContainerBytes() uint64 { return r.containerBytes }

// PayloadBytes is the concatenated block payload size.
func (r *StreamReader) PayloadBytes() uint64 { return r.payloadBytes }

// SourceHash is the BLAKE3 of the logical stream (verified by the caller).
func (r *StreamReader) SourceHash() [32]byte { return r.sourceHash }

// DirectoryHash is the BLAKE3 of the serialized directory.
func (r *StreamReader) DirectoryHash() [32]byte { return r.directoryHash }

func (r *StreamReader) blockIndex(pos uint64) (int, error) {
	if pos >= r.logicalLen {
		return 0, coreTooShort(pos+1, r.logicalLen)
	}
	if r.uniform {
		return int(pos / uint64(r.blockBytes)), nil
	}
	i := sort.Search(len(r.entries), func(i int) bool { return r.entries[i].LogicalStart > pos })
	idx := i - 1
	if idx < 0 {
		return 0, badSection("stream: no block covers offset")
	}
	e := r.entries[idx]
	if pos >= e.LogicalStart+e.LogicalLen {
		return 0, badSection("stream: no block covers offset")
	}
	return idx, nil
}

func (r *StreamReader) decodeBlock(idx int, local, take uint64) ([]byte, error) {
	e := r.entries[idx]
	payload := r.data[e.PhysicalOffset : e.PhysicalOffset+e.PhysicalLen]
	if streamContentID(payload) != e.ContentID {
		return nil, &Error{Kind: KindChecksumMismatch, Section: "stream block"}
	}
	if e.MachineID == MachineStore {
		end := local + take
		if end > uint64(len(payload)) {
			return nil, badSection("stream: raw block is shorter than its directory entry")
		}
		out := make([]byte, take)
		copy(out, payload[local:end])
		return out, nil
	}
	m, _ := machineByID(e.MachineID)
	ctx, err := prepare(m, payload)
	if err != nil {
		return nil, err
	}
	s, err := m.Init(payload)
	if err != nil {
		return nil, err
	}
	for i := uint64(0); i < local; i++ {
		if _, err := ctx.reveal(payload, &s); err != nil {
			return nil, err
		}
	}
	out := make([]byte, take)
	for i := uint64(0); i < take; i++ {
		b, err := ctx.reveal(payload, &s)
		if err != nil {
			return nil, err
		}
		out[i] = b
	}
	return out, nil
}

// ReadAt reads len(buf) logical bytes at offset into buf.
func (r *StreamReader) ReadAt(offset uint64, buf []byte) error {
	end := offset + uint64(len(buf))
	if end < offset {
		return overflow("stream read range")
	}
	if end > r.logicalLen {
		return outOfBounds(offset, uint64(len(buf)), r.logicalLen)
	}
	if len(buf) == 0 {
		return nil
	}
	pos := offset
	filled := 0
	for filled < len(buf) {
		idx, err := r.blockIndex(pos)
		if err != nil {
			return err
		}
		e := r.entries[idx]
		local := pos - e.LogicalStart
		avail := e.LogicalLen - local
		t := uint64(len(buf) - filled)
		if t > avail {
			t = avail
		}
		tmp, err := r.decodeBlock(idx, local, t)
		if err != nil {
			return err
		}
		copy(buf[filled:], tmp)
		filled += int(t)
		pos += t
	}
	return nil
}

// ReadSequential reads the next chunk sequentially, caching at most one block.
func (r *StreamReader) ReadSequential(buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	pos := r.seqPos
	if pos >= r.logicalLen {
		return 0, nil
	}
	want := int(r.logicalLen - pos)
	if want > len(buf) {
		want = len(buf)
	}
	var cache []byte
	cacheIdx := -1
	filled := 0
	for filled < want {
		idx, err := r.blockIndex(pos)
		if err != nil {
			return 0, err
		}
		e := r.entries[idx]
		if cacheIdx != idx {
			if err := checkDefaultAllocation(e.LogicalLen); err != nil {
				return 0, err
			}
			cache, err = r.decodeBlock(idx, 0, e.LogicalLen)
			if err != nil {
				return 0, err
			}
			cacheIdx = idx
		}
		local := pos - e.LogicalStart
		avail := int(e.LogicalLen - local)
		t := want - filled
		if t > avail {
			t = avail
		}
		copy(buf[filled:], cache[local:local+uint64(t)])
		filled += t
		pos += uint64(t)
	}
	r.seqPos = pos
	return filled, nil
}

func crc32IEEE(b []byte) uint32 {
	return crc32.ChecksumIEEE(b)
}
