package ekbloom

// nl2 is OrANS-NL2 (machine_id = 1): an expansion machine with V = N.
//
// Genesis Omega_0 = (0, 0). Step at cursor p: s = S[h]^l^core[p],
// x' = (l<<8)|s, p' = p+1, where h = x>>8 and l = x&0xff.
type nl2 struct{}

func (nl2) ID() uint32 { return MachineNL2 }

func (nl2) Init(core []byte) (Snapshot, error) { return Snapshot{}, nil }

func (nl2) Reveal(core []byte, s *Snapshot) (byte, error) {
	p := s.Cursor
	if p >= uint64(len(core)) {
		return 0, coreTooShort(p+1, uint64(len(core)))
	}
	r := core[p]
	h := byte(s.State >> 8)
	l := byte(s.State)
	sym := sbox[h] ^ l ^ r
	s.State = uint32(l)<<8 | uint32(sym)
	s.Cursor = p + 1
	return sym, nil
}

func (nl2) Peek(core []byte, s *Snapshot) (byte, error) {
	p := s.Cursor
	if p >= uint64(len(core)) {
		return 0, coreTooShort(p+1, uint64(len(core)))
	}
	h := byte(s.State >> 8)
	l := byte(s.State)
	return sbox[h] ^ l ^ core[p], nil
}

func (nl2) Encode(source []byte) ([]byte, error) {
	out := make([]byte, len(source))
	var x uint32
	for i, sym := range source {
		h := byte(x >> 8)
		l := byte(x)
		out[i] = sym ^ sbox[h] ^ l
		x = uint32(l)<<8 | uint32(sym)
	}
	return out, nil
}

func (nl2) CheckCore(core []byte, logicalLen uint64) error {
	if logicalLen > uint64(len(core)) {
		return coreTooShort(logicalLen, uint64(len(core)))
	}
	return nil
}

// store is OrANS-Store (machine_id = 3): the verbatim identity codec.
//
// Genesis Omega_0 = (0, 0). A step reveals core[cursor], sets state = symbol,
// and advances cursor. V = N exactly.
type store struct{}

func (store) ID() uint32 { return MachineStore }

func (store) Init(core []byte) (Snapshot, error) { return Snapshot{}, nil }

func (store) Reveal(core []byte, s *Snapshot) (byte, error) {
	p := s.Cursor
	if p >= uint64(len(core)) {
		return 0, coreTooShort(p+1, uint64(len(core)))
	}
	sym := core[p]
	s.State = uint32(sym)
	s.Cursor = p + 1
	return sym, nil
}

func (store) Peek(core []byte, s *Snapshot) (byte, error) {
	p := s.Cursor
	if p >= uint64(len(core)) {
		return 0, coreTooShort(p+1, uint64(len(core)))
	}
	return core[p], nil
}

func (store) Encode(source []byte) ([]byte, error) {
	out := make([]byte, len(source))
	copy(out, source)
	return out, nil
}

func (store) CheckCore(core []byte, logicalLen uint64) error {
	if logicalLen > uint64(len(core)) {
		return coreTooShort(logicalLen, uint64(len(core)))
	}
	return nil
}
