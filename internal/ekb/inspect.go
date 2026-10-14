package ekb

import "encoding/binary"

// ComponentReport is one manifest accounting entry.
type ComponentReport struct {
	Name  string
	Kind  string
	Bytes uint64
}

// Report is a full manifestation accounting report.
type Report struct {
	LogicalLen     uint64
	VisibleBytes   uint64
	PhysicalBytes  uint64
	ConcealedBytes uint64
	ExpansionRatio float64
	Generation     uint64
	Profiles       []string
	Components     []ComponentReport
}

var supportedIntervals = map[uint32]bool{65536: true, 4096: true, 1024: true, 256: true, 64: true, 16: true, 4: true, 1: true}

// checkpointLayout is the decoded header of a checkpoint-set payload.
type checkpointLayout struct {
	interval  uint32
	count     uint64
	headerLen int
	entryLen  int
	packed    bool
	wide      bool
}

func parseCheckpointLayout(b []byte) (checkpointLayout, error) {
	var out checkpointLayout
	if len(b) >= 8 && [8]byte(b[0:8]) == [8]byte{'E', 'K', 'B', 'C', 'K', 'P', 'T', 0} {
		const fixedLen = 8 + 1 + 1 + 4 + 8
		if len(b) < fixedLen {
			return out, truncated(fixedLen, uint64(len(b)))
		}
		if b[8] != 2 {
			return out, badSection("unsupported checkpoint version")
		}
		switch b[9] {
		case 1:
			out.packed = true
		case 2:
		case 3:
			out.wide = true
		default:
			return out, badSection("unknown checkpoint format")
		}
		out.interval = binary.LittleEndian.Uint32(b[10:])
		if !supportedIntervals[out.interval] {
			return out, &Error{Kind: KindBadInterval, Offset: uint64(out.interval)}
		}
		out.count = binary.LittleEndian.Uint64(b[14:])
		if out.count == 0 {
			return out, badSection("checkpoint set count is zero")
		}
		if _, ok := mulOverflow(out.count-1, uint64(out.interval)); ok {
			return out, overflow("checkpoint span")
		}
		out.headerLen = fixedLen
		switch {
		case out.packed:
			out.entryLen = 2
		case out.wide:
			out.entryLen = 45
		default:
			out.entryLen = 12
		}
		return out, nil
	}
	const headerLen = 4 + 8
	if len(b) < headerLen {
		return out, truncated(headerLen, uint64(len(b)))
	}
	out.interval = binary.LittleEndian.Uint32(b[0:])
	if !supportedIntervals[out.interval] {
		return out, &Error{Kind: KindBadInterval, Offset: uint64(out.interval)}
	}
	out.count = binary.LittleEndian.Uint64(b[4:])
	if out.count == 0 {
		return out, badSection("checkpoint set count is zero")
	}
	out.headerLen = headerLen
	out.entryLen = 12
	return out, nil
}

func mulOverflow(a, b uint64) (uint64, bool) {
	if a == 0 || b == 0 {
		return 0, false
	}
	p := a * b
	return p, p/b != a
}

// historyComponent validates a HistoryFull payload header and returns its state
// projection bytes (count * 4).
func historyComponent(b []byte) (uint64, error) {
	const headerLen = 8 + 8 + 8 + 32 + 8
	if len(b) < headerLen {
		return 0, truncated(headerLen, uint64(len(b)))
	}
	logicalLen := binary.LittleEndian.Uint64(b[8:])
	count := binary.LittleEndian.Uint64(b[56:])
	if count != logicalLen+1 {
		return 0, badSection("history count inconsistent with logical_len")
	}
	if count > (^uint64(0))/4 {
		return 0, overflow("history byte length")
	}
	return count * 4, nil
}

// tilesComponent parses the fixed prefix and directory of a TileSet payload and
// returns its manifested bytes (directory + tile blob).
func tilesComponent(b []byte) (uint64, error) {
	const prefixLen = 96
	if len(b) < prefixLen {
		return 0, truncated(prefixLen, uint64(len(b)))
	}
	if [8]byte(b[0:8]) != [8]byte{'E', 'K', 'B', 'T', 'I', 'L', 'E', 'S'} {
		return 0, badSection("tile set magic mismatch")
	}
	kind := b[10]
	pos := prefixLen
	readVec := func() (int, error) {
		if pos+8 > len(b) {
			return 0, truncated(uint64(pos+8), uint64(len(b)))
		}
		n := binary.LittleEndian.Uint64(b[pos:])
		pos += 8
		if n > uint64(len(b)) {
			return 0, overflow("tile directory vector length")
		}
		pos += int(n) * 4
		if pos > len(b) {
			return 0, truncated(uint64(pos), uint64(len(b)))
		}
		return int(n), nil
	}
	var dirEntries int
	switch kind {
	case 0:
		n, err := readVec()
		if err != nil {
			return 0, err
		}
		dirEntries = n
	case 1:
		g, err := readVec()
		if err != nil {
			return 0, err
		}
		s, err := readVec()
		if err != nil {
			return 0, err
		}
		dirEntries = g + s
	default:
		return 0, badSection("unknown tile directory kind")
	}
	if pos+8 > len(b) {
		return 0, truncated(uint64(pos+8), uint64(len(b)))
	}
	dataLen := binary.LittleEndian.Uint64(b[pos:])
	return uint64(dirEntries)*4 + dataLen, nil
}

// blocksComponent parses a BlockIndex payload prefix and returns its manifested
// bytes (fixed prefix + restart records).
func blocksComponent(b []byte) (uint64, error) {
	const prefixLen = 104
	if len(b) < prefixLen {
		return 0, truncated(prefixLen, uint64(len(b)))
	}
	if [8]byte(b[0:8]) != [8]byte{'E', 'K', 'B', 'K', 'B', 'L', 'K', 'S'} {
		return 0, badSection("block index magic mismatch")
	}
	format := b[9]
	machineID := binary.LittleEndian.Uint32(b[10:])
	blockBytes := binary.LittleEndian.Uint32(b[14:])
	if blockBytes == 0 {
		return 0, badSection("block size out of range")
	}
	logicalLen := binary.LittleEndian.Uint64(b[18:])
	count := logicalLen/uint64(blockBytes) + 1
	var entryLen uint64
	switch format {
	case 1:
		entryLen = 2
	case 2:
		entryLen = 12
	case 3:
		entryLen = 45
	default:
		return 0, badSection("unknown block index format")
	}
	_ = machineID
	return prefixLen + count*entryLen, nil
}

// accountStructures computes the physical component accounting for a scanned
// file, mirroring the Rust `FileBody::inspect`.
func accountStructures(header GenesisHeader, commits []ScannedCommit, data []byte) ([]ComponentReport, error) {
	var components []ComponentReport
	components = append(components, ComponentReport{Name: "core", Kind: "core", Bytes: header.CoreLen})
	for _, c := range commits {
		for _, s := range c.Sections {
			payload := data[s.PayloadOffset : s.PayloadOffset+s.PayloadLen]
			switch s.Kind {
			case SectionHistoryFull:
				n, err := historyComponent(payload)
				if err != nil {
					return nil, err
				}
				components = append(components, ComponentReport{Name: "history", Kind: "history", Bytes: n})
			case SectionCheckpoints:
				cp, err := parseCheckpointLayout(payload)
				if err != nil {
					return nil, err
				}
				components = append(components, ComponentReport{Name: "checkpoint", Kind: "checkpoint", Bytes: cp.count * uint64(cp.entryLen)})
			case SectionBloomGlobal, SectionBloomContext, SectionBloomRegion, SectionBloomPath:
				if s.PayloadLen < 74 {
					return nil, truncated(74, s.PayloadLen)
				}
				components = append(components, ComponentReport{Name: "bloom", Kind: "bloom", Bytes: s.PayloadLen - 74})
			case SectionCertificate:
				components = append(components, ComponentReport{Name: "certificate", Kind: "certificate", Bytes: s.PayloadLen})
			case SectionHistorySegmented:
				components = append(components, ComponentReport{Name: "history-segmented", Kind: "history-segmented", Bytes: s.PayloadLen})
			case SectionTiles:
				n, err := tilesComponent(payload)
				if err != nil {
					return nil, err
				}
				components = append(components, ComponentReport{Name: "tiles", Kind: "tiles", Bytes: n})
			case SectionBlocks:
				n, err := blocksComponent(payload)
				if err != nil {
					return nil, err
				}
				components = append(components, ComponentReport{Name: "blocks", Kind: "blocks", Bytes: n})
			}
		}
	}
	return components, nil
}
