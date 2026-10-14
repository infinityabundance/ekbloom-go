package ekb

import "encoding/binary"

// fastpath holds the manifested structures that accelerate reads, parsed once at
// Open. Any of them may be absent; a read falls back to authoritative machine
// decode (and, for repeated random access, a lazily materialised symbol array).
//
// All three are exact by construction in the reference: the path bloom `hot`
// surface is the authoritative output; a full history gives the machine-1 state
// at any position; a checkpoint set bounds a replay to less than its interval.
type fastpath struct {
	// pathHot is the exact output surface of a certified path bloom (machine 1).
	pathHot []byte
	// histStates[i] = x_i for a full machine-1 history (len == logical+1).
	histStates []uint32
	// checkpoints is a compact machine-1 restart set.
	checkpoints *ckptSet
}

type ckptSet struct {
	interval uint64
	count    uint64
	states   []uint16 // packed machine-1 states, cursor = j*interval
}

// parseFastPaths scans every section for a usable manifested structure. Only
// structures bound to the newest core hash are used, so a stale or mismatched
// structure is ignored rather than trusted.
func parseFastPaths(header GenesisHeader, commits []ScannedCommit, data []byte, coreHash [32]byte) *fastpath {
	fp := &fastpath{}
	for _, c := range commits {
		for _, s := range c.Sections {
			payload := data[s.PayloadOffset : s.PayloadOffset+s.PayloadLen]
			switch s.Kind {
			case SectionBloomPath:
				if fp.pathHot == nil {
					if hot, ok := parsePathBloomHot(payload, header.LogicalSymbols, coreHash); ok {
						fp.pathHot = hot
					}
				}
			case SectionHistoryFull:
				if fp.histStates == nil {
					if st, ok := parseHistoryStates(payload, header.LogicalSymbols, coreHash); ok {
						fp.histStates = st
					}
				}
			case SectionCheckpoints:
				if fp.checkpoints == nil {
					if cs, ok := parseCheckpointSet(payload); ok {
						fp.checkpoints = cs
					}
				}
			}
		}
	}
	return fp
}

const shellHeaderLen = 74

// parsePathBloomHot returns the exact output surface of a Certified Path bloom
// shell bound to the expected core hash.
func parsePathBloomHot(shell []byte, logicalLen uint64, coreHash [32]byte) ([]byte, bool) {
	if len(shell) < shellHeaderLen {
		return nil, false
	}
	kind := binary.LittleEndian.Uint32(shell[0:])
	if kind != uint32(3) { // BloomKind::Path
		return nil, false
	}
	status := binary.LittleEndian.Uint32(shell[30:])
	if status != 1 { // Certification::Certified
		return nil, false
	}
	var shellHash [32]byte
	copy(shellHash[:], shell[34:66])
	if shellHash != coreHash {
		return nil, false
	}
	shellLogical := binary.LittleEndian.Uint64(shell[14:])
	entries := binary.LittleEndian.Uint64(shell[22:])
	declared := binary.LittleEndian.Uint64(shell[66:])
	payloadLen := uint64(len(shell) - shellHeaderLen)
	if shellLogical != logicalLen || declared != payloadLen {
		return nil, false
	}
	p := shell[shellHeaderLen:]
	if uint64(len(p)) < 18 {
		return nil, false
	}
	depth := binary.LittleEndian.Uint16(p[0:])
	if depth == 0 {
		return nil, false
	}
	pllogical := binary.LittleEndian.Uint64(p[2:])
	nblocks := binary.LittleEndian.Uint64(p[10:])
	if pllogical != logicalLen {
		return nil, false
	}
	wantBlocks := (logicalLen + uint64(depth) - 1) / uint64(depth)
	if nblocks != wantBlocks || entries != nblocks {
		return nil, false
	}
	need := uint64(18) + logicalLen + nblocks*4
	if uint64(len(p)) != need {
		return nil, false
	}
	hot := p[18 : 18+logicalLen]
	return hot, true
}

// parseHistoryStates returns the machine-1 state projection of a full history
// bound to the expected core hash.
func parseHistoryStates(payload []byte, logicalLen uint64, coreHash [32]byte) ([]uint32, bool) {
	const headerLen = 8 + 8 + 8 + 32 + 8
	if uint64(len(payload)) < headerLen {
		return nil, false
	}
	var histHash [32]byte
	copy(histHash[:], payload[24:56])
	if histHash != coreHash {
		return nil, false
	}
	hLogical := binary.LittleEndian.Uint64(payload[8:])
	count := binary.LittleEndian.Uint64(payload[56:])
	if hLogical != logicalLen || count != logicalLen+1 {
		return nil, false
	}
	need := headerLen + count*4
	if uint64(len(payload)) != need {
		return nil, false
	}
	states := make([]uint32, count)
	for i := uint64(0); i < count; i++ {
		states[i] = binary.LittleEndian.Uint32(payload[headerLen+i*4:])
	}
	return states, true
}

// parseCheckpointSet returns a compact packed machine-1 restart set.
func parseCheckpointSet(payload []byte) (*ckptSet, bool) {
	layout, err := parseCheckpointLayout(payload)
	if err != nil || !layout.packed {
		return nil, false
	}
	need := uint64(layout.headerLen) + layout.count*2
	if uint64(len(payload)) != need {
		return nil, false
	}
	states := make([]uint16, layout.count)
	for j := uint64(0); j < layout.count; j++ {
		states[j] = binary.LittleEndian.Uint16(payload[uint64(layout.headerLen)+j*2:])
	}
	return &ckptSet{interval: uint64(layout.interval), count: layout.count, states: states}, true
}

// statesAt returns the machine-1 state x_pos if a fast path covers it.
// ok is false when no structure can answer directly.
func (fp *fastpath) stateAt(pos uint64) (uint32, bool) {
	if fp.histStates != nil && pos < uint64(len(fp.histStates)) {
		return fp.histStates[pos], true
	}
	return 0, false
}

// ---- accelerated reads ----------------------------------------------------

// readPathBloom copies the requested range from the exact path surface.
func (r *Reader) readPathBloom(off, end uint64) ([]byte, bool) {
	if r.fast.pathHot == nil || end > uint64(len(r.fast.pathHot)) {
		return nil, false
	}
	out := make([]byte, end-off)
	copy(out, r.fast.pathHot[off:end])
	return out, true
}

// byteAtHistory reads one symbol from a full machine-1 history in O(1).
func (r *Reader) byteAtHistory(pos uint64) (byte, bool) {
	if r.machine.ID() != MachineNL2 {
		return 0, false
	}
	x, ok := r.fast.stateAt(pos)
	if !ok {
		return 0, false
	}
	return nl2Reveal(x, r.core[pos]), true
}

// nl2Reveal computes the machine-1 symbol for state x and core byte r.
func nl2Reveal(x uint32, coreByte byte) byte {
	h := byte(x >> 8)
	l := byte(x)
	return sbox[h] ^ l ^ coreByte
}

// readHistory decodes a range from a full machine-1 history, O(1) per symbol.
func (r *Reader) readHistory(off, end uint64) ([]byte, bool) {
	if r.machine.ID() != MachineNL2 || r.fast.histStates == nil || end > uint64(len(r.fast.histStates)) {
		return nil, false
	}
	out := make([]byte, end-off)
	for i := off; i < end; i++ {
		out[i-off] = nl2Reveal(r.fast.histStates[i], r.core[i])
	}
	return out, true
}

// readCheckpoint decodes a range from the nearest packed restart, replaying at
// most `interval` steps. It never allocates more than the requested range.
func (r *Reader) readCheckpoint(off, end uint64) ([]byte, bool) {
	cs := r.fast.checkpoints
	if cs == nil || cs.interval == 0 || r.machine.ID() != MachineNL2 || end > r.logical {
		return nil, false
	}
	j := off / cs.interval
	if j >= cs.count {
		return nil, false
	}
	x := uint32(cs.states[j])
	cursor := j * cs.interval
	out := make([]byte, end-off)
	for i := off; i < end; i++ {
		// replay forward to i
		for cursor < i {
			s := nl2Reveal(x, r.core[cursor])
			x = uint32(byte(x))<<8 | uint32(s)
			cursor++
		}
		out[i-off] = nl2Reveal(x, r.core[i])
	}
	return out, true
}
