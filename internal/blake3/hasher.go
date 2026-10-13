package blake3

// Hasher is a streaming BLAKE3 hasher (unkeyed). It holds at most one 64-byte
// block plus a small chaining-value stack, so it hashes arbitrarily large inputs
// under bounded memory.
type Hasher struct {
	key      [8]uint32
	flags    uint32
	chunk    chunkState
	chunkIdx uint64
	stack    [64][8]uint32
	stackLen int
}

// NewHasher returns a fresh unkeyed streaming hasher.
func NewHasher() *Hasher {
	h := &Hasher{key: iv}
	h.chunk = chunkState{cv: iv}
	return h
}

// NewKeyedHasher returns a fresh keyed streaming hasher.
func NewKeyedHasher(key [32]byte) *Hasher {
	var kw [8]uint32
	for i := 0; i < 8; i++ {
		kw[i] = uint32(key[i*4]) | uint32(key[i*4+1])<<8 |
			uint32(key[i*4+2])<<16 | uint32(key[i*4+3])<<24
	}
	h := &Hasher{key: kw, flags: flagKeyedHash}
	h.chunk = chunkState{cv: kw, flags: flagKeyedHash}
	return h
}

func (h *Hasher) addChunkCV(cv [8]uint32, totalChunks int) {
	for totalChunks&1 == 0 {
		h.stackLen--
		cv = parentOutput(h.stack[h.stackLen], cv, h.key, h.flags).chainingValue()
		totalChunks >>= 1
	}
	h.stack[h.stackLen] = cv
	h.stackLen++
}

// Write appends data to the hash. It never returns an error.
func (h *Hasher) Write(data []byte) (int, error) {
	n := len(data)
	for len(data) > 0 {
		filled := h.chunk.blocks*blockLen + h.chunk.blockLen
		if filled == chunkLen {
			cv := h.chunk.output().chainingValue()
			h.chunkIdx++
			h.addChunkCV(cv, int(h.chunkIdx))
			h.chunk = chunkState{cv: h.key, counter: h.chunkIdx, flags: h.flags}
			filled = 0
		}
		take := chunkLen - filled
		if take > len(data) {
			take = len(data)
		}
		h.chunk.update(data[:take])
		data = data[take:]
	}
	return n, nil
}

// Sum returns the 32-byte digest of everything written so far. It does not reset
// the hasher; call it once.
func (h *Hasher) Sum() [32]byte {
	out := h.chunk.output()
	for i := h.stackLen - 1; i >= 0; i-- {
		out = parentOutput(h.stack[i], out.chainingValue(), h.key, h.flags)
	}
	return out.rootBytes()
}
