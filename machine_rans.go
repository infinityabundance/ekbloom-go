package ekbloom

import (
	"encoding/binary"
	"sort"
)

// Order-0 rANS constants (shared by machines 2, 4, 7, 8 and the machine-9 model).
const (
	ransL           = uint32(1) << 23
	ransWL          = uint32(1) << 15 // machine 7 word-renorm lower bound
	scaleBits       = 12
	ransM           = uint32(1) << scaleBits // 4096
	ransSymbols     = 256
	ransModelBytes  = ransSymbols * 2    // 512
	ransHeaderBytes = ransModelBytes + 4 // 516
	maxStepsPerByte = uint64(4096)
)

// model is a validated order-0 byte model: scaled frequencies and prefix sums.
type model struct {
	freq [ransSymbols]uint16
	cum  [ransSymbols]uint32
}

// modelFromSource builds the scaled model for source by largest-remainder
// scaling, identical to the Rust `Model::from_source`.
func modelFromSource(source []byte) model {
	var count [ransSymbols]uint64
	for _, b := range source {
		count[b]++
	}
	var clamped [ransSymbols]uint64
	var total uint64
	for i := 0; i < ransSymbols; i++ {
		c := count[i]
		if c < 1 {
			c = 1
		}
		clamped[i] = c
		total += c
	}
	const budget = uint64(ransM) - ransSymbols // 3840

	var rem, frac [ransSymbols]uint64
	var assigned uint64
	for i := 0; i < ransSymbols; i++ {
		num := clamped[i] * budget
		rem[i] = num / total
		frac[i] = num % total
		assigned += rem[i]
	}
	leftover := budget - assigned
	order := make([]int, ransSymbols)
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		ia, ib := order[a], order[b]
		if frac[ia] != frac[ib] {
			return frac[ia] > frac[ib]
		}
		return ia < ib
	})
	for k := 0; k < int(leftover) && k < len(order); k++ {
		rem[order[k]]++
	}

	var m model
	var acc uint32
	for i := 0; i < ransSymbols; i++ {
		f := uint16(rem[i] + 1)
		m.freq[i] = f
		m.cum[i] = acc
		acc += uint32(f)
	}
	return m
}

// modelFromHeader parses and validates the model stored in core's header.
func modelFromHeader(core []byte) (model, error) {
	var m model
	if len(core) < ransHeaderBytes {
		return m, truncated(ransHeaderBytes, uint64(len(core)))
	}
	var sum uint32
	for i := 0; i < ransSymbols; i++ {
		v := binary.LittleEndian.Uint16(core[i*2:])
		m.freq[i] = v
		sum += uint32(v)
		if v == 0 {
			return m, badSection("rans model contains a zero frequency")
		}
	}
	if sum != ransM {
		return m, badSection("rans model frequencies do not sum to 4096")
	}
	var acc uint32
	for i := 0; i < ransSymbols; i++ {
		m.cum[i] = acc
		acc += uint32(m.freq[i])
	}
	return m, nil
}

// initialState returns the u32 coder state stored in the model header.
func modelInitialState(core []byte) uint32 {
	return binary.LittleEndian.Uint32(core[ransModelBytes:])
}

// ransContext is a validated, reusable order-0 decoder context for one core.
type ransContext struct {
	entries []uint32 // 4096 fused (freq | cum<<12 | symbol<<24) entries
}

func ransContextFromCore(core []byte) (*ransContext, error) {
	m, err := modelFromHeader(core)
	if err != nil {
		return nil, err
	}
	entries := make([]uint32, ransM)
	for s := 0; s < ransSymbols; s++ {
		f := uint32(m.freq[s])
		c := m.cum[s]
		packed := f | (c << scaleBits) | (uint32(s) << 24)
		for i := uint32(0); i < f; i++ {
			entries[c+i] = packed
		}
	}
	return &ransContext{entries: entries}, nil
}

func (c *ransContext) reveal(core []byte, s *Snapshot) (byte, error) {
	x := s.State
	slot := x & (ransM - 1)
	e := c.entries[slot]
	nx := (x>>scaleBits)*(e&0x0FFF) + slot - ((e >> scaleBits) & 0x0FFF)
	p := s.Cursor
	for nx < ransL {
		if p >= uint64(len(core)) {
			return 0, coreTooShort(p+1, uint64(len(core)))
		}
		nx = (nx << 8) | uint32(core[p])
		p++
	}
	s.State = nx
	s.Cursor = p
	return byte(e >> 24), nil
}

func (c *ransContext) peek(core []byte, s *Snapshot) (byte, error) {
	probe := *s
	return c.reveal(core, &probe)
}

// rans0 is OrANS-RANS0 (machine_id = 2): order-0 rANS with 8-bit renorm bytes.
type rans0 struct{}

func (rans0) ID() uint32 { return MachineRans0 }

func (rans0) Init(core []byte) (Snapshot, error) {
	if _, err := modelFromHeader(core); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{State: modelInitialState(core), Cursor: ransHeaderBytes}, nil
}

func (rans0) Reveal(core []byte, s *Snapshot) (byte, error) {
	ctx, err := ransContextFromCore(core)
	if err != nil {
		return 0, err
	}
	return ctx.reveal(core, s)
}

func (rans0) Peek(core []byte, s *Snapshot) (byte, error) {
	ctx, err := ransContextFromCore(core)
	if err != nil {
		return 0, err
	}
	return ctx.peek(core, s)
}

func (rans0) Encode(source []byte) ([]byte, error) {
	m := modelFromSource(source)
	x := ransL
	emitted := make([]byte, 0, len(source))
	for i := len(source) - 1; i >= 0; i-- {
		sym := source[i]
		f := uint64(m.freq[sym])
		c := uint64(m.cum[sym])
		xmax := uint64(ransL>>scaleBits<<8) * f
		for uint64(x) >= xmax {
			emitted = append(emitted, byte(x))
			x >>= 8
		}
		xu := uint64(x)
		x = uint32((xu/f)<<scaleBits + xu%f + c)
	}
	out := make([]byte, 0, ransHeaderBytes+len(emitted))
	for i := 0; i < ransSymbols; i++ {
		out = appendU16(out, m.freq[i])
	}
	out = appendU32(out, x)
	for i := len(emitted) - 1; i >= 0; i-- {
		out = append(out, emitted[i])
	}
	return out, nil
}

func ransStepCap(core []byte) uint64 {
	return uint64(len(core))*maxStepsPerByte + (1 << 20)
}

func (rans0) CheckCore(core []byte, logicalLen uint64) error {
	ctx, err := ransContextFromCore(core)
	if err != nil {
		return err
	}
	s, _ := rans0{}.Init(core)
	cap := ransStepCap(core)
	for steps := uint64(0); steps < logicalLen; steps++ {
		if steps >= cap {
			return &Error{Kind: KindNonTermination}
		}
		if _, err := ctx.reveal(core, &s); err != nil {
			return err
		}
	}
	return nil
}

func appendU16(dst []byte, v uint16) []byte {
	var t [2]byte
	binary.LittleEndian.PutUint16(t[:], v)
	return append(dst, t[:]...)
}

// rans0w is OrANS-RANS0W (machine_id = 7): order-0 rANS with 16-bit renorm words.
type rans0w struct{}

func (rans0w) ID() uint32 { return MachineRans0W }

func (rans0w) Init(core []byte) (Snapshot, error) {
	if _, err := modelFromHeader(core); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{State: modelInitialState(core), Cursor: ransHeaderBytes}, nil
}

func (rans0w) Reveal(core []byte, s *Snapshot) (byte, error) {
	ctx, err := ransWContextFromCore(core)
	if err != nil {
		return 0, err
	}
	return ctx.reveal(core, s)
}

func (rans0w) Peek(core []byte, s *Snapshot) (byte, error) {
	ctx, err := ransWContextFromCore(core)
	if err != nil {
		return 0, err
	}
	return ctx.peek(core, s)
}

func (rans0w) Encode(source []byte) ([]byte, error) {
	m := modelFromSource(source)
	x := ransWL
	emitted := make([]uint16, 0, len(source))
	for i := len(source) - 1; i >= 0; i-- {
		sym := source[i]
		f := uint64(m.freq[sym])
		c := uint64(m.cum[sym])
		xmax := uint64(ransWL>>scaleBits<<16) * f
		xu := uint64(x)
		for xu >= xmax {
			emitted = append(emitted, uint16(xu))
			xu >>= 16
		}
		x = uint32((xu/f)<<scaleBits + xu%f + c)
	}
	out := make([]byte, 0, ransHeaderBytes+2*len(emitted))
	for i := 0; i < ransSymbols; i++ {
		out = appendU16(out, m.freq[i])
	}
	out = appendU32(out, x)
	for i := len(emitted) - 1; i >= 0; i-- {
		out = appendU16(out, emitted[i])
	}
	return out, nil
}

func (rans0w) CheckCore(core []byte, logicalLen uint64) error {
	ctx, err := ransWContextFromCore(core)
	if err != nil {
		return err
	}
	s, _ := rans0w{}.Init(core)
	cap := ransStepCap(core)
	for steps := uint64(0); steps < logicalLen; steps++ {
		if steps >= cap {
			return &Error{Kind: KindNonTermination}
		}
		if _, err := ctx.reveal(core, &s); err != nil {
			return err
		}
	}
	return nil
}

// ransWContext is the prepared word-renorm decoder for machine 7.
type ransWContext struct {
	entries []uint32
}

func ransWContextFromCore(core []byte) (*ransWContext, error) {
	ctx, err := ransContextFromCore(core)
	if err != nil {
		return nil, err
	}
	return &ransWContext{entries: ctx.entries}, nil
}

func readWordLE(core []byte, p uint64) (uint16, error) {
	if p+2 > uint64(len(core)) {
		return 0, coreTooShort(p+2, uint64(len(core)))
	}
	return binary.LittleEndian.Uint16(core[p:]), nil
}

func (c *ransWContext) reveal(core []byte, s *Snapshot) (byte, error) {
	x := s.State
	slot := x & (ransM - 1)
	e := c.entries[slot]
	nx := (x>>scaleBits)*(e&0x0FFF) + slot - ((e >> scaleBits) & 0x0FFF)
	p := s.Cursor
	if nx < ransWL {
		w, err := readWordLE(core, p)
		if err != nil {
			return 0, err
		}
		nx = (nx << 16) | uint32(w)
		p += 2
	}
	s.State = nx
	s.Cursor = p
	return byte(e >> 24), nil
}

func (c *ransWContext) peek(core []byte, s *Snapshot) (byte, error) {
	probe := *s
	return c.reveal(core, &probe)
}
