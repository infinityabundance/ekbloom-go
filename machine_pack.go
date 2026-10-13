package ekbloom

import "encoding/binary"

// Fixed-width packing (machine 5) constants.
const (
	packMinBits     = 1
	packMaxBits     = 8
	packAlphabetOff = 2
	packLenTrailer  = 8
)

func packBitMask(bits uint8) uint32 {
	if bits >= 8 {
		return 0xFF
	}
	return (uint32(1) << bits) - 1
}

func packPayloadLen(n uint64, bits uint8, k int) uint64 {
	if k == 1 || n == 0 {
		return 0
	}
	return (n*uint64(bits) + 7) / 8
}

// pack is OrANS-PACK (machine_id = 5).
type pack struct{}

func (pack) ID() uint32 { return MachinePack }

type packLayout struct {
	k        int
	alphabet [256]byte
}

func packDiscover(source []byte) packLayout {
	var seen [256]bool
	k := 0
	for _, b := range source {
		if !seen[b] {
			seen[b] = true
			k++
		}
	}
	var l packLayout
	l.k = k
	idx := 0
	for v := 0; v < 256; v++ {
		if seen[v] {
			l.alphabet[idx] = byte(v)
			idx++
		}
	}
	return l
}

func packCore(source []byte, bits uint8, l packLayout) []byte {
	k := l.k
	n := uint64(len(source))
	headerLen := packAlphabetOff + k + packLenTrailer
	payload := packPayloadLen(n, bits, k)
	out := make([]byte, 0, headerLen+int(payload))
	out = append(out, bits, byte(k))
	if k > 0 {
		out = append(out, l.alphabet[:k]...)
	}
	out = appendU64(out, n)
	if k >= 2 {
		var indexOf [256]byte
		for i := 0; i < k; i++ {
			indexOf[l.alphabet[i]] = byte(i)
		}
		var acc, held uint32
		for _, s := range source {
			acc |= uint32(indexOf[s]) << held
			held += uint32(bits)
			for held >= 8 {
				out = append(out, byte(acc))
				acc >>= 8
				held -= 8
			}
		}
		if held > 0 {
			out = append(out, byte(acc))
		}
	}
	return out
}

func packIdentityCore(source []byte) []byte {
	out := make([]byte, 0, packAlphabetOff+packLenTrailer+len(source))
	out = append(out, 8, 0)
	out = appendU64(out, uint64(len(source)))
	out = append(out, source...)
	return out
}

func (pack) Encode(source []byte) ([]byte, error) {
	l := packDiscover(source)
	if l.k == 0 || l.k == 256 {
		return packIdentityCore(source), nil
	}
	var bits uint8
	switch {
	case l.k <= 2:
		bits = 1
	case l.k <= 4:
		bits = 2
	case l.k <= 16:
		bits = 4
	default:
		bits = 8
	}
	packed := packCore(source, bits, l)
	if len(packed) < len(source) {
		return packed, nil
	}
	return packIdentityCore(source), nil
}

// packContext is the prepared decoder for machine 5.
type packContext struct {
	bits       uint8
	k          int
	alphabet   [256]byte
	logicalLen uint64
	headerLen  int
	payloadLen int
}

func packContextFromCore(core []byte) (*packContext, error) {
	if len(core) < packAlphabetOff {
		return nil, truncated(packAlphabetOff, uint64(len(core)))
	}
	bits := core[0]
	if bits < packMinBits || bits > packMaxBits {
		return nil, badSection("packed symbol width out of range [1, 8]")
	}
	k := int(core[1])
	if k > (1 << bits) {
		return nil, badSection("packed alphabet size exceeds 2^bits")
	}
	if k == 0 && bits != 8 {
		return nil, badSection("identity packed alphabet requires bits = 8")
	}
	alphaLen := 0
	if k > 0 {
		alphaLen = k
	}
	nOff := packAlphabetOff + alphaLen
	headerLen := nOff + packLenTrailer
	if len(core) < headerLen {
		return nil, truncated(uint64(headerLen), uint64(len(core)))
	}
	c := &packContext{bits: bits, k: k, headerLen: headerLen}
	if k >= 2 {
		var seen [256]bool
		for i := 0; i < k; i++ {
			sym := core[packAlphabetOff+i]
			if seen[sym] {
				return nil, badSection("packed alphabet has a duplicate symbol")
			}
			seen[sym] = true
			c.alphabet[i] = sym
		}
	} else if k == 1 {
		c.alphabet[0] = core[packAlphabetOff]
	}
	c.logicalLen = binary.LittleEndian.Uint64(core[nOff:])
	c.payloadLen = int(packPayloadLen(c.logicalLen, bits, k))
	need := headerLen + c.payloadLen
	if len(core) < need {
		return nil, truncated(uint64(need), uint64(len(core)))
	}
	if len(core) > need {
		return nil, badSection("packed core has trailing bytes")
	}
	return c, nil
}

func (c *packContext) byteAt(core []byte, i uint64) (byte, error) {
	if i >= c.logicalLen {
		return 0, coreTooShort(i+1, c.logicalLen)
	}
	if c.k == 1 {
		return c.alphabet[0], nil
	}
	bit := i * uint64(c.bits)
	byteOff := int(bit >> 3)
	off := uint32(bit & 7)
	payload := core[c.headerLen : c.headerLen+c.payloadLen]
	lo := uint32(payload[byteOff])
	var idx uint32
	if off+uint32(c.bits) <= 8 {
		idx = (lo >> off) & packBitMask(c.bits)
	} else {
		var hi uint32
		if byteOff+1 < len(payload) {
			hi = uint32(payload[byteOff+1])
		}
		idx = ((lo >> off) | (hi << (8 - off))) & packBitMask(c.bits)
	}
	if c.k == 0 {
		return byte(idx), nil
	}
	if int(idx) >= c.k {
		return 0, badSection("packed index outside alphabet")
	}
	return c.alphabet[idx], nil
}

func (c *packContext) reveal(core []byte, s *Snapshot) (byte, error) {
	sym, err := c.byteAt(core, s.Cursor)
	if err != nil {
		return 0, err
	}
	s.State = uint32(sym)
	s.Cursor++
	return sym, nil
}

func (c *packContext) peek(core []byte, s *Snapshot) (byte, error) {
	return c.byteAt(core, s.Cursor)
}

func (pack) Init(core []byte) (Snapshot, error) {
	if _, err := packContextFromCore(core); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{}, nil
}

func (pack) Reveal(core []byte, s *Snapshot) (byte, error) {
	c, err := packContextFromCore(core)
	if err != nil {
		return 0, err
	}
	return c.reveal(core, s)
}

func (pack) Peek(core []byte, s *Snapshot) (byte, error) {
	c, err := packContextFromCore(core)
	if err != nil {
		return 0, err
	}
	return c.peek(core, s)
}

func (pack) CheckCore(core []byte, logicalLen uint64) error {
	c, err := packContextFromCore(core)
	if err != nil {
		return err
	}
	if c.logicalLen != logicalLen {
		return badSection("packed core declares a different symbol count")
	}
	return nil
}
