package ekbloom

import "encoding/binary"

// Segmented container (machine 6) constants.
var segMagic = [4]byte{'E', 'S', 'E', 'G'}

const (
	segVersion          = 1
	segHeaderBytes      = 96
	segEntryBytes       = 56
	segWindowEntryBytes = 8
	segKindConstant     = uint32(0)
	segNoModel          = uint32(0xFFFFFFFF)
	segDefaultSegBytes  = 4096
	segFlagVariable     = uint16(0x0001)
	segMinWindowShift   = 8
	segMaxShift         = 62
)

// Header field offsets.
const (
	hMagic          = 0
	hVersion        = 4
	hFlags          = 6
	hSegmentCount   = 8
	hEntryBytes     = 12
	hSegShift       = 16
	hWindowShift    = 17
	hLogicalLen     = 24
	hDirectoryBytes = 32
	hPayloadOffset  = 40
	hPayloadBytes   = 48
	hDirectoryHash  = 56
)

// Directory entry field offsets.
const (
	eLogicalStart   = 0
	eLogicalLen     = 8
	ePhysicalOffset = 16
	ePhysicalLen    = 24
	eMachineID      = 32
	eModelRef       = 36
	eContentID      = 40
)

// seg is OrANS-SEG (machine_id = 6).
type seg struct{}

func (seg) ID() uint32 { return MachineSeg }

func segContentID(b []byte) [16]byte {
	h := blake3Hash(b)
	return [16]byte(h[:16])
}

func ceilLog2(x uint64) uint8 {
	v := x
	if v > 0 {
		v--
	}
	var s uint8
	for v > 0 {
		v >>= 1
		s++
	}
	return s
}

type segEntry struct {
	logicalStart   uint64
	logicalLen     uint64
	physicalOffset uint64
	physicalLen    uint64
	machineID      uint32
	modelRef       uint32
	contentID      [16]byte
}

func writeSegEntry(dir []byte, i int, e segEntry) {
	o := i * segEntryBytes
	binary.LittleEndian.PutUint64(dir[o+eLogicalStart:], e.logicalStart)
	binary.LittleEndian.PutUint64(dir[o+eLogicalLen:], e.logicalLen)
	binary.LittleEndian.PutUint64(dir[o+ePhysicalOffset:], e.physicalOffset)
	binary.LittleEndian.PutUint64(dir[o+ePhysicalLen:], e.physicalLen)
	binary.LittleEndian.PutUint32(dir[o+eMachineID:], e.machineID)
	binary.LittleEndian.PutUint32(dir[o+eModelRef:], e.modelRef)
	copy(dir[o+eContentID:o+eContentID+16], e.contentID[:])
}

func parseSegEntry(core []byte, o int) segEntry {
	var content [16]byte
	copy(content[:], core[o+eContentID:o+eContentID+16])
	return segEntry{
		logicalStart:   binary.LittleEndian.Uint64(core[o+eLogicalStart:]),
		logicalLen:     binary.LittleEndian.Uint64(core[o+eLogicalLen:]),
		physicalOffset: binary.LittleEndian.Uint64(core[o+ePhysicalOffset:]),
		physicalLen:    binary.LittleEndian.Uint64(core[o+ePhysicalLen:]),
		machineID:      binary.LittleEndian.Uint32(core[o+eMachineID:]),
		modelRef:       binary.LittleEndian.Uint32(core[o+eModelRef:]),
		contentID:      content,
	}
}

func segChooseRep(seg []byte) (uint32, []byte, error) {
	if len(seg) == 0 {
		return segKindConstant, []byte{0}, nil
	}
	first := seg[0]
	allSame := true
	for _, b := range seg {
		if b != first {
			allSame = false
			break
		}
	}
	if allSame {
		return segKindConstant, []byte{first}, nil
	}
	packed, err := pack{}.Encode(seg)
	if err != nil {
		return 0, nil, err
	}
	if len(packed) < len(seg) {
		return MachinePack, packed, nil
	}
	cp := make([]byte, len(seg))
	copy(cp, seg)
	return MachineStore, cp, nil
}

type segMode struct {
	variable    bool
	shift       uint8
	windowShift uint8
}

func segBuildContainer(source []byte, lens []uint64, mode segMode) ([]byte, error) {
	var totalLen uint64
	for _, l := range lens {
		totalLen += l
	}
	count := len(lens)
	windowCount := 0
	if mode.variable && totalLen > 0 {
		windowCount = int(((totalLen - 1) >> mode.windowShift) + 1)
	}
	directoryBytes := count*segEntryBytes + windowCount*segWindowEntryBytes
	payloadOffset := segHeaderBytes + directoryBytes

	entries := make([]segEntry, 0, count)
	payloads := make([][]byte, 0, count)
	start := uint64(0)
	phys := uint64(payloadOffset)
	for _, ln := range lens {
		mid, payload, err := segChooseRep(source[start : start+ln])
		if err != nil {
			return nil, err
		}
		entries = append(entries, segEntry{
			logicalStart:   start,
			logicalLen:     ln,
			physicalOffset: phys,
			physicalLen:    uint64(len(payload)),
			machineID:      mid,
			modelRef:       segNoModel,
			contentID:      segContentID(payload),
		})
		phys += uint64(len(payload))
		payloads = append(payloads, payload)
		start += ln
	}
	payloadBytes := phys - uint64(payloadOffset)

	type win struct{ first, cnt uint32 }
	windows := make([]win, windowCount)
	if mode.variable {
		for i, e := range entries {
			g := int(e.logicalStart >> mode.windowShift)
			if windows[g].cnt == 0 {
				windows[g].first = uint32(i)
			}
			windows[g].cnt++
		}
	}

	body := make([]byte, directoryBytes)
	for i, e := range entries {
		writeSegEntry(body, i, e)
	}
	for g, w := range windows {
		o := count*segEntryBytes + g*segWindowEntryBytes
		binary.LittleEndian.PutUint32(body[o:], w.first)
		binary.LittleEndian.PutUint32(body[o+4:], w.cnt)
	}
	for _, p := range payloads {
		body = append(body, p...)
	}
	directoryHash := blake3Hash(body)

	header := make([]byte, segHeaderBytes)
	copy(header[hMagic:], segMagic[:])
	binary.LittleEndian.PutUint16(header[hVersion:], segVersion)
	var flags uint16
	if mode.variable {
		flags = segFlagVariable
	}
	binary.LittleEndian.PutUint16(header[hFlags:], flags)
	binary.LittleEndian.PutUint32(header[hSegmentCount:], uint32(count))
	binary.LittleEndian.PutUint32(header[hEntryBytes:], segEntryBytes)
	if !mode.variable {
		header[hSegShift] = mode.shift
	}
	header[hWindowShift] = mode.windowShift
	binary.LittleEndian.PutUint64(header[hLogicalLen:], totalLen)
	binary.LittleEndian.PutUint64(header[hDirectoryBytes:], uint64(directoryBytes))
	binary.LittleEndian.PutUint64(header[hPayloadOffset:], uint64(payloadOffset))
	binary.LittleEndian.PutUint64(header[hPayloadBytes:], payloadBytes)
	copy(header[hDirectoryHash:hDirectoryHash+32], directoryHash[:])

	out := make([]byte, 0, segHeaderBytes+len(body))
	out = append(out, header...)
	out = append(out, body...)
	return out, nil
}

func (seg) Encode(source []byte) ([]byte, error) { return seg{}.encodeSegmented(source) }

func (seg) encodeSegmented(source []byte) ([]byte, error) {
	const segSpan = segDefaultSegBytes
	var lens []uint64
	i := 0
	for i < len(source) {
		ln := segSpan
		if len(source)-i < segSpan {
			ln = len(source) - i
		}
		lens = append(lens, uint64(ln))
		i += ln
	}
	return segBuildContainer(source, lens, segMode{shift: 12})
}

// segContext is the prepared decoder for machine 6.
type segContext struct {
	logicalLen     uint64
	variable       bool
	segShift       uint8
	windowShift    uint8
	directoryBytes uint64
	payloadOffset  uint64
	directoryHash  [32]byte
	entries        []segEntry
	windows        [][2]uint32
	packed         []*packContext
}

func segContextFromCore(core []byte) (*segContext, error) {
	if len(core) < segHeaderBytes {
		return nil, truncated(segHeaderBytes, uint64(len(core)))
	}
	if [4]byte(core[hMagic:hMagic+4]) != segMagic {
		return nil, badSection("machine 6 core: bad segmented magic")
	}
	if binary.LittleEndian.Uint16(core[hVersion:]) != segVersion {
		return nil, badSection("machine 6 core: unsupported segment version")
	}
	flags := binary.LittleEndian.Uint16(core[hFlags:])
	if flags & ^uint16(segFlagVariable) != 0 {
		return nil, badSection("machine 6 core: unknown segment flags")
	}
	variable := flags&segFlagVariable != 0
	if binary.LittleEndian.Uint32(core[hEntryBytes:]) != segEntryBytes {
		return nil, badSection("machine 6 core: entry_bytes mismatch")
	}
	segShift := core[hSegShift]
	windowShift := core[hWindowShift]
	if variable {
		if windowShift < 1 || windowShift > segMaxShift {
			return nil, badSection("machine 6 core: window_shift out of range")
		}
	} else if segShift < 1 || segShift > segMaxShift {
		return nil, badSection("machine 6 core: seg_shift out of range")
	}
	count := int(binary.LittleEndian.Uint32(core[hSegmentCount:]))
	logicalLen := binary.LittleEndian.Uint64(core[hLogicalLen:])
	directoryBytes := binary.LittleEndian.Uint64(core[hDirectoryBytes:])
	payloadOffset := binary.LittleEndian.Uint64(core[hPayloadOffset:])
	payloadBytes := binary.LittleEndian.Uint64(core[hPayloadBytes:])

	windowCount := 0
	if variable && logicalLen > 0 {
		windowCount = int(((logicalLen - 1) >> windowShift) + 1)
	}
	expectedDir := count*segEntryBytes + windowCount*segWindowEntryBytes
	if directoryBytes != uint64(expectedDir) {
		return nil, badSection("machine 6 core: directory bytes mismatch")
	}
	if payloadOffset != uint64(segHeaderBytes)+directoryBytes {
		return nil, badSection("machine 6 core: payload offset inconsistent")
	}
	if payloadOffset+payloadBytes != uint64(len(core)) {
		return nil, badSection("machine 6 core: payload length inconsistent with core")
	}
	dirOff := segHeaderBytes
	dirEnd := dirOff + int(directoryBytes)
	if dirEnd > len(core) {
		return nil, truncated(uint64(dirEnd), uint64(len(core)))
	}
	var storedHash [32]byte
	copy(storedHash[:], core[hDirectoryHash:hDirectoryHash+32])
	if blake3Hash(core[dirOff:]) != storedHash {
		return nil, &Error{Kind: KindChecksumMismatch, Section: "machine 6 directory"}
	}

	c := &segContext{
		logicalLen:     logicalLen,
		variable:       variable,
		segShift:       segShift,
		windowShift:    windowShift,
		directoryBytes: directoryBytes,
		payloadOffset:  payloadOffset,
		directoryHash:  storedHash,
	}
	payloadRegionEnd := payloadOffset + payloadBytes
	var logicalCursor uint64
	physCursor := payloadOffset
	for i := 0; i < count; i++ {
		e := parseSegEntry(core, dirOff+i*segEntryBytes)
		if e.logicalStart != logicalCursor {
			return nil, badSection("machine 6 core: segment logical_start mismatch")
		}
		if e.logicalLen == 0 {
			return nil, badSection("machine 6 core: segment has zero length")
		}
		logicalCursor += e.logicalLen
		if logicalCursor > logicalLen {
			return nil, badSection("machine 6 core: segment over-declares logical length")
		}
		if e.modelRef != segNoModel {
			return nil, badSection("machine 6 core: unsupported model reference")
		}
		if e.physicalLen == 0 {
			return nil, badSection("machine 6 core: segment has empty payload")
		}
		pEnd := e.physicalOffset + e.physicalLen
		if e.physicalOffset < payloadOffset || e.physicalOffset < physCursor || pEnd > payloadRegionEnd {
			return nil, badSection("machine 6 core: segment physical range out of range")
		}
		payload := core[e.physicalOffset:pEnd]
		if segContentID(payload) != e.contentID {
			return nil, badSection("machine 6 core: segment content id mismatch")
		}
		switch e.machineID {
		case segKindConstant:
			if e.physicalLen != 1 {
				return nil, badSection("machine 6 core: constant segment payload is not one byte")
			}
			c.packed = append(c.packed, nil)
		case MachineStore:
			if e.physicalLen != e.logicalLen {
				return nil, badSection("machine 6 core: raw segment length mismatch")
			}
			c.packed = append(c.packed, nil)
		case MachinePack:
			ctx, err := packContextFromCore(payload)
			if err != nil {
				return nil, err
			}
			if ctx.logicalLen != e.logicalLen {
				return nil, badSection("machine 6 core: packed segment length mismatch")
			}
			c.packed = append(c.packed, ctx)
		default:
			return nil, badSection("machine 6 core: unsupported segment machine id")
		}
		physCursor = pEnd
		c.entries = append(c.entries, e)
	}
	if logicalCursor != logicalLen {
		return nil, badSection("machine 6 core: segments do not cover the logical length")
	}

	if variable {
		wOff := dirOff + count*segEntryBytes
		segSize := uint64(1) << windowShift
		var expectedFirst uint32
		for g := 0; g < windowCount; g++ {
			o := wOff + g*segWindowEntryBytes
			first := binary.LittleEndian.Uint32(core[o:])
			cnt := binary.LittleEndian.Uint32(core[o+4:])
			if cnt == 0 || first != expectedFirst {
				return nil, badSection("machine 6 core: window index inconsistent")
			}
			end := int(first + cnt)
			if end > count {
				return nil, badSection("machine 6 core: window overruns the directory")
			}
			if c.entries[first].logicalStart != uint64(g)*segSize {
				return nil, badSection("machine 6 core: window does not start at its boundary")
			}
			for i := int(first); i < end; i++ {
				if (c.entries[i].logicalStart >> windowShift) != uint64(g) {
					return nil, badSection("machine 6 core: segment crosses a window boundary")
				}
			}
			expectedFirst = uint32(end)
			c.windows = append(c.windows, [2]uint32{first, cnt})
		}
		if int(expectedFirst) != count {
			return nil, badSection("machine 6 core: window index does not cover the directory")
		}
	} else {
		segSize := uint64(1) << segShift
		for i, e := range c.entries {
			if e.logicalStart != uint64(i)*segSize {
				return nil, badSection("machine 6 core: fixed segment start mismatch")
			}
			var expectedLen uint64
			if i+1 == count {
				expectedLen = logicalLen - uint64(i)*segSize
			} else {
				expectedLen = segSize
			}
			if e.logicalLen != expectedLen {
				return nil, badSection("machine 6 core: fixed segment length mismatch")
			}
		}
	}
	return c, nil
}

func (c *segContext) locate(offset uint64) (int, error) {
	if offset >= c.logicalLen {
		return 0, coreTooShort(offset+1, c.logicalLen)
	}
	if !c.variable {
		idx := int(offset >> c.segShift)
		if idx >= len(c.entries) {
			return 0, badSection("machine 6 core: fixed segment index outside the directory")
		}
		e := c.entries[idx]
		if offset < e.logicalStart || offset >= e.logicalStart+e.logicalLen {
			return 0, badSection("machine 6 core: fixed segment does not cover the offset")
		}
		return idx, nil
	}
	g := int(offset >> c.windowShift)
	if g >= len(c.windows) {
		return 0, badSection("machine 6 core: window index outside the directory")
	}
	w := c.windows[g]
	for i := int(w[0]); i < int(w[0]+w[1]); i++ {
		e := c.entries[i]
		if offset >= e.logicalStart && offset < e.logicalStart+e.logicalLen {
			return i, nil
		}
	}
	return 0, badSection("machine 6 core: no segment covers the offset")
}

func (c *segContext) byteAt(core []byte, offset uint64) (byte, error) {
	idx, err := c.locate(offset)
	if err != nil {
		return 0, err
	}
	e := c.entries[idx]
	local := offset - e.logicalStart
	switch e.machineID {
	case segKindConstant:
		if int(e.physicalOffset) >= len(core) {
			return 0, badSection("machine 6 core: constant payload missing")
		}
		return core[e.physicalOffset], nil
	case MachineStore:
		p := e.physicalOffset + local
		if p >= uint64(len(core)) {
			return 0, badSection("machine 6 core: raw payload missing")
		}
		return core[p], nil
	case MachinePack:
		ctx := c.packed[idx]
		if ctx == nil {
			return 0, badSection("machine 6 core: packed context missing")
		}
		payload := core[e.physicalOffset : e.physicalOffset+e.physicalLen]
		return ctx.byteAt(payload, local)
	}
	return 0, badSection("machine 6 core: unsupported segment machine")
}

func (c *segContext) reveal(core []byte, s *Snapshot) (byte, error) {
	sym, err := c.byteAt(core, s.Cursor)
	if err != nil {
		return 0, err
	}
	s.State = uint32(sym)
	s.Cursor++
	return sym, nil
}

func (c *segContext) peek(core []byte, s *Snapshot) (byte, error) {
	return c.byteAt(core, s.Cursor)
}

func (seg) Init(core []byte) (Snapshot, error) {
	if _, err := segContextFromCore(core); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{}, nil
}

func (seg) Reveal(core []byte, s *Snapshot) (byte, error) {
	c, err := segContextFromCore(core)
	if err != nil {
		return 0, err
	}
	return c.reveal(core, s)
}

func (seg) Peek(core []byte, s *Snapshot) (byte, error) {
	c, err := segContextFromCore(core)
	if err != nil {
		return 0, err
	}
	return c.peek(core, s)
}

func (seg) CheckCore(core []byte, logicalLen uint64) error {
	c, err := segContextFromCore(core)
	if err != nil {
		return err
	}
	if c.logicalLen != logicalLen {
		return badSection("segmented core declares a different symbol count")
	}
	return nil
}
