package ekb

// Interleaved order-0 rANS (machine 4) and independent-lane rANS (machine 8)
// share the order-0 model and the fused decode table with machine 2.

const ransIFieldOffset = ransModelBytes      // 512: the K field
const ransIStatesOffset = ransModelBytes + 4 // 516: the lane states

// defaultK is the lane count used by machine 4's Encode.
const defaultK = 4

func validState(x uint32) bool { return x >= ransL && x < ransL<<8 }

// rans0i is OrANS-RANS0I (machine_id = 4).
type rans0i struct{}

func (rans0i) ID() uint32 { return MachineRans0I }

// ransIContext is the prepared interleaved decoder context for one core.
type ransIContext struct {
	entries     []uint32
	k           int
	headerBytes uint64
}

func ransIContextFromCore(core []byte) (*ransIContext, error) {
	if len(core) < ransIStatesOffset {
		return nil, truncated(ransIStatesOffset, uint64(len(core)))
	}
	k := int(binaryLE32(core[ransIFieldOffset:]))
	if k < 1 || k > maxLanes {
		return nil, badSection("interleaved rANS lane count out of range [1, 8]")
	}
	header := uint64(ransIStatesOffset + 4*k)
	if uint64(len(core)) < header {
		return nil, truncated(header, uint64(len(core)))
	}
	base, err := ransContextFromCore(core)
	if err != nil {
		return nil, err
	}
	for i := 0; i < k; i++ {
		x := binaryLE32(core[ransIStatesOffset+4*i:])
		if !validState(x) {
			return nil, badSection("interleaved rANS lane state out of range")
		}
	}
	return &ransIContext{entries: base.entries, k: k, headerBytes: header}, nil
}

func (c *ransIContext) genesis(core []byte) Snapshot {
	var s Snapshot
	for i := 0; i < c.k; i++ {
		s.Lanes[i] = binaryLE32(core[ransIStatesOffset+4*i:])
	}
	s.Cursor = c.headerBytes
	s.Lane = 0
	s.State = s.Lanes[0]
	return s
}

func (c *ransIContext) step(core []byte, x uint32, p *uint64) (byte, uint32, error) {
	slot := x & (ransM - 1)
	e := c.entries[slot]
	nx := (x>>scaleBits)*(e&0x0FFF) + slot - ((e >> scaleBits) & 0x0FFF)
	for nx < ransL {
		if *p >= uint64(len(core)) {
			return 0, 0, coreTooShort(*p+1, uint64(len(core)))
		}
		nx = (nx << 8) | uint32(core[*p])
		*p++
	}
	return byte(e >> 24), nx, nil
}

func (c *ransIContext) reveal(core []byte, s *Snapshot) (byte, error) {
	lane := int(s.Lane) % c.k
	sym, nx, err := c.step(core, s.Lanes[lane], &s.Cursor)
	if err != nil {
		return 0, err
	}
	s.Lanes[lane] = nx
	if lane+1 == c.k {
		s.Lane = 0
	} else {
		s.Lane = uint8(lane + 1)
	}
	s.State = s.Lanes[s.Lane]
	return sym, nil
}

func (c *ransIContext) peek(core []byte, s *Snapshot) (byte, error) {
	probe := *s
	return c.reveal(core, &probe)
}

func (rans0i) Init(core []byte) (Snapshot, error) {
	c, err := ransIContextFromCore(core)
	if err != nil {
		return Snapshot{}, err
	}
	return c.genesis(core), nil
}

func (rans0i) Reveal(core []byte, s *Snapshot) (byte, error) {
	c, err := ransIContextFromCore(core)
	if err != nil {
		return 0, err
	}
	return c.reveal(core, s)
}

func (rans0i) Peek(core []byte, s *Snapshot) (byte, error) {
	c, err := ransIContextFromCore(core)
	if err != nil {
		return 0, err
	}
	return c.peek(core, s)
}

func (rans0i) Encode(source []byte) ([]byte, error) { return encodeWithK(source, defaultK) }

func (rans0i) CheckCore(core []byte, logicalLen uint64) error {
	c, err := ransIContextFromCore(core)
	if err != nil {
		return err
	}
	s := c.genesis(core)
	cap := ransStepCap(core)
	for steps := uint64(0); steps < logicalLen; steps++ {
		if steps >= cap {
			return &Error{Kind: KindNonTermination}
		}
		if _, err := c.reveal(core, &s); err != nil {
			return err
		}
	}
	return nil
}

// encodeWithK encodes source with k interleaved lanes.
func encodeWithK(source []byte, k int) ([]byte, error) {
	if k < 1 || k > maxLanes {
		return nil, &Error{Kind: KindInvalidArgument, Reason: "interleaved rans lane count out of range"}
	}
	m := modelFromSource(source)
	var states [maxLanes]uint32
	for i := range states {
		states[i] = ransL
	}
	emitted := make([]byte, 0, len(source))
	for i := len(source) - 1; i >= 0; i-- {
		lane := i % k
		sym := source[i]
		f := uint64(m.freq[sym])
		c := uint64(m.cum[sym])
		x := uint64(states[lane])
		xmax := uint64(ransL>>scaleBits<<8) * f
		for x >= xmax {
			emitted = append(emitted, byte(x))
			x >>= 8
		}
		states[lane] = uint32((x/f)<<scaleBits + x%f + c)
	}
	header := ransIStatesOffset + 4*k
	out := make([]byte, 0, header+len(emitted))
	for i := 0; i < ransSymbols; i++ {
		out = appendU16(out, m.freq[i])
	}
	out = appendU32(out, uint32(k))
	for i := 0; i < k; i++ {
		out = appendU32(out, states[i])
	}
	for i := len(emitted) - 1; i >= 0; i-- {
		out = append(out, emitted[i])
	}
	return out, nil
}

// ---- machine 8: independent-lane streams ---------------------------------

const ilFixedHeader = ransModelBytes + 4 // 516
const defaultILK = 4

// rans0il is OrANS-RANS0IL (machine_id = 8).
type rans0il struct{}

func (rans0il) ID() uint32 { return MachineRans0IL }

type ilContext struct {
	entries     []uint32
	k           int
	headerBytes uint64
	starts      [maxLanes]uint64
	ends        [maxLanes]uint64
}

func ilContextFromCore(core []byte) (*ilContext, error) {
	if len(core) < ilFixedHeader {
		return nil, truncated(ilFixedHeader, uint64(len(core)))
	}
	k := int(binaryLE32(core[ransIFieldOffset:]))
	if k < 1 || k > maxLanes {
		return nil, badSection("independent-lane count out of range [1, 8]")
	}
	header := uint64(ilFixedHeader + 8*k)
	if uint64(len(core)) < header {
		return nil, truncated(header, uint64(len(core)))
	}
	base, err := ransContextFromCore(core)
	if err != nil {
		return nil, err
	}
	c := &ilContext{entries: base.entries, k: k, headerBytes: header}
	acc := header
	for t := 0; t < k; t++ {
		laneLen := uint64(binaryLE32(core[ilFixedHeader+4*t:]))
		c.starts[t] = acc
		acc += laneLen
		c.ends[t] = acc
	}
	if acc > uint64(len(core)) {
		return nil, coreTooShort(acc, uint64(len(core)))
	}
	if acc != uint64(len(core)) {
		return nil, badSection("independent-lane payloads do not fill the core")
	}
	for t := 0; t < k; t++ {
		x := binaryLE32(core[ilFixedHeader+4*k+4*t:])
		if !validState(x) {
			return nil, badSection("independent-lane state out of range")
		}
	}
	return c, nil
}

func (c *ilContext) genesis(core []byte) Snapshot {
	var s Snapshot
	for t := 0; t < c.k; t++ {
		s.Lanes[t] = binaryLE32(core[ilFixedHeader+4*c.k+4*t:])
		s.Cursors[t] = c.starts[t]
	}
	s.Lane = 0
	s.State = s.Lanes[0]
	s.Cursor = s.Cursors[0]
	return s
}

func (c *ilContext) step(core []byte, lane int, x uint32, p *uint64) (byte, uint32, error) {
	end := c.ends[lane]
	slot := x & (ransM - 1)
	e := c.entries[slot]
	nx := (x>>scaleBits)*(e&0x0FFF) + slot - ((e >> scaleBits) & 0x0FFF)
	for nx < ransL {
		if *p >= end {
			return 0, 0, coreTooShort(*p+1, uint64(len(core)))
		}
		nx = (nx << 8) | uint32(core[*p])
		*p++
	}
	return byte(e >> 24), nx, nil
}

func (c *ilContext) reveal(core []byte, s *Snapshot) (byte, error) {
	lane := int(s.Lane) % c.k
	sym, nx, err := c.step(core, lane, s.Lanes[lane], &s.Cursors[lane])
	if err != nil {
		return 0, err
	}
	s.Lanes[lane] = nx
	if lane+1 == c.k {
		s.Lane = 0
	} else {
		s.Lane = uint8(lane + 1)
	}
	return sym, nil
}

func (c *ilContext) peek(core []byte, s *Snapshot) (byte, error) {
	probe := *s
	return c.reveal(core, &probe)
}

func (rans0il) Init(core []byte) (Snapshot, error) {
	c, err := ilContextFromCore(core)
	if err != nil {
		return Snapshot{}, err
	}
	return c.genesis(core), nil
}

func (rans0il) Reveal(core []byte, s *Snapshot) (byte, error) {
	c, err := ilContextFromCore(core)
	if err != nil {
		return 0, err
	}
	return c.reveal(core, s)
}

func (rans0il) Peek(core []byte, s *Snapshot) (byte, error) {
	c, err := ilContextFromCore(core)
	if err != nil {
		return 0, err
	}
	return c.peek(core, s)
}

func (rans0il) Encode(source []byte) ([]byte, error) { return encodeIL(source, defaultILK) }

func (rans0il) CheckCore(core []byte, logicalLen uint64) error {
	c, err := ilContextFromCore(core)
	if err != nil {
		return err
	}
	s := c.genesis(core)
	cap := ransStepCap(core)
	for steps := uint64(0); steps < logicalLen; steps++ {
		if steps >= cap {
			return &Error{Kind: KindNonTermination}
		}
		if _, err := c.reveal(core, &s); err != nil {
			return err
		}
	}
	return nil
}

func encodeIL(source []byte, k int) ([]byte, error) {
	if k < 1 || k > maxLanes {
		return nil, &Error{Kind: KindInvalidArgument, Reason: "independent-lane count out of range"}
	}
	m := modelFromSource(source)
	var states [maxLanes]uint32
	for i := range states {
		states[i] = ransL
	}
	emitted := make([][]byte, k)
	for i := len(source) - 1; i >= 0; i-- {
		lane := i % k
		sym := source[i]
		f := uint64(m.freq[sym])
		c := uint64(m.cum[sym])
		x := uint64(states[lane])
		xmax := uint64(ransL>>scaleBits<<8) * f
		for x >= xmax {
			emitted[lane] = append(emitted[lane], byte(x))
			x >>= 8
		}
		states[lane] = uint32((x/f)<<scaleBits + x%f + c)
	}
	header := ilFixedHeader + 8*k
	payload := 0
	for _, e := range emitted {
		payload += len(e)
	}
	out := make([]byte, 0, header+payload)
	for i := 0; i < ransSymbols; i++ {
		out = appendU16(out, m.freq[i])
	}
	out = appendU32(out, uint32(k))
	for lane := 0; lane < k; lane++ {
		out = appendU32(out, uint32(len(emitted[lane])))
	}
	for lane := 0; lane < k; lane++ {
		out = appendU32(out, states[lane])
	}
	for lane := 0; lane < k; lane++ {
		for i := len(emitted[lane]) - 1; i >= 0; i-- {
			out = append(out, emitted[lane][i])
		}
	}
	return out, nil
}

func binaryLE32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}
