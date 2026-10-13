// Package blake3 is a dependency-free, pure-Go implementation of the BLAKE3
// cryptographic hash (unkeyed and keyed) that is byte-for-byte identical to the
// reference Rust `blake3` crate used by the EKBLOOM reference implementation.
//
// It contains no `unsafe` and no cgo, so it builds and runs on every Go target
// with an identical result (a determinism requirement for the EKBLOOM wire
// format). Correctness is pinned by known-answer tests and by vectors generated
// directly from the Rust `blake3` crate (see `blake3_test.go` and `testdata/`).
//
// Only the 32-byte fixed-width output used by EKBLOOM is exposed: `Sum` (unkeyed)
// and `SumKeyed` (keyed). Both are pure functions with no shared state.
package blake3

import "math/bits"

// Domain-separated BLAKE3 flags (from the BLAKE3 specification).
const (
	flagChunkStart = 1 << 0
	flagChunkEnd   = 1 << 1
	flagParent     = 1 << 2
	flagRoot       = 1 << 3
	flagKeyedHash  = 1 << 4
)

// Block and chunk geometry.
const (
	blockLen = 64
	chunkLen = 1024
	outLen   = 32
)

// iv is the BLAKE3 initialisation vector (the SHA-256 IV).
var iv = [8]uint32{
	0x6A09E667, 0xBB67AE85, 0x3C6EF372, 0xA54FF53A,
	0x510E527F, 0x9B05688C, 0x1F83D9AB, 0x5BE0CD19,
}

// msgPermutation is the BLAKE3 message schedule permutation applied between
// rounds.
var msgPermutation = [16]int{2, 6, 3, 10, 7, 0, 4, 13, 1, 11, 12, 5, 9, 14, 15, 8}

// Sum returns the 32-byte BLAKE3 hash of data (unkeyed).
func Sum(data []byte) [outLen]byte {
	return hashAll(data, iv, 0)
}

// SumKeyed returns the 32-byte BLAKE3 keyed hash of data under key.
func SumKeyed(key [32]byte, data []byte) [outLen]byte {
	var kw [8]uint32
	for i := 0; i < 8; i++ {
		kw[i] = uint32(key[i*4]) | uint32(key[i*4+1])<<8 |
			uint32(key[i*4+2])<<16 | uint32(key[i*4+3])<<24
	}
	return hashAll(data, kw, flagKeyedHash)
}

// g is the BLAKE3 quarter-round mixing function.
func g(s *[16]uint32, a, b, c, d int, mx, my uint32) {
	s[a] = s[a] + s[b] + mx
	s[d] = bits.RotateLeft32(s[d]^s[a], -16)
	s[c] = s[c] + s[d]
	s[b] = bits.RotateLeft32(s[b]^s[c], -12)
	s[a] = s[a] + s[b] + my
	s[d] = bits.RotateLeft32(s[d]^s[a], -8)
	s[c] = s[c] + s[d]
	s[b] = bits.RotateLeft32(s[b]^s[c], -7)
}

// round is one BLAKE3 round over the state with message schedule m.
func round(s *[16]uint32, m *[16]uint32) {
	g(s, 0, 4, 8, 12, m[0], m[1])
	g(s, 1, 5, 9, 13, m[2], m[3])
	g(s, 2, 6, 10, 14, m[4], m[5])
	g(s, 3, 7, 11, 15, m[6], m[7])
	g(s, 0, 5, 10, 15, m[8], m[9])
	g(s, 1, 6, 11, 12, m[10], m[11])
	g(s, 2, 7, 8, 13, m[12], m[13])
	g(s, 3, 4, 9, 14, m[14], m[15])
}

// compress is the BLAKE3 compression function.
func compress(cv [8]uint32, m [16]uint32, counter uint64, blen, flags uint32) [16]uint32 {
	s := [16]uint32{
		cv[0], cv[1], cv[2], cv[3], cv[4], cv[5], cv[6], cv[7],
		iv[0], iv[1], iv[2], iv[3],
		uint32(counter), uint32(counter >> 32), blen, flags,
	}
	mm := m
	for r := 0; r < 7; r++ {
		round(&s, &mm)
		if r < 6 {
			var next [16]uint32
			for i := 0; i < 16; i++ {
				next[i] = mm[msgPermutation[i]]
			}
			mm = next
		}
	}
	var out [16]uint32
	for i := 0; i < 8; i++ {
		out[i] = s[i] ^ s[i+8]
		out[i+8] = s[i+8] ^ cv[i]
	}
	return out
}

// blockWords reads up to 16 little-endian words from b, zero-padding the tail.
func blockWords(b []byte) [16]uint32 {
	var w [16]uint32
	for i := 0; i < 16; i++ {
		o := i * 4
		if o+4 <= len(b) {
			w[i] = uint32(b[o]) | uint32(b[o+1])<<8 | uint32(b[o+2])<<16 | uint32(b[o+3])<<24
		} else {
			var tmp [4]byte
			for j := 0; o+j < len(b); j++ {
				tmp[j] = b[o+j]
			}
			w[i] = uint32(tmp[0]) | uint32(tmp[1])<<8 | uint32(tmp[2])<<16 | uint32(tmp[3])<<24
		}
	}
	return w
}

// chunkState accumulates a single 1024-byte chunk.
type chunkState struct {
	cv       [8]uint32
	counter  uint64
	block    [64]byte
	blockLen int
	blocks   int
	flags    uint32
}

func (c *chunkState) startFlag() uint32 {
	if c.blocks == 0 {
		return flagChunkStart
	}
	return 0
}

func (c *chunkState) update(input []byte) {
	for len(input) > 0 {
		if c.blockLen == blockLen {
			c.cv = chunkCVOf(c.cv, blockWords(c.block[:]), c.counter, blockLen, c.startFlag()|c.flags)
			c.blocks++
			c.blockLen = 0
		}
		n := blockLen - c.blockLen
		if n > len(input) {
			n = len(input)
		}
		copy(c.block[c.blockLen:], input[:n])
		c.blockLen += n
		input = input[n:]
	}
}

func (c *chunkState) output() output {
	return output{
		inputCV:  c.cv,
		block:    blockWords(c.block[:c.blockLen]),
		counter:  c.counter,
		blockLen: uint32(c.blockLen),
		flags:    c.flags | c.startFlag() | flagChunkEnd,
	}
}

// output is an unfinalised BLAKE3 output node.
type output struct {
	inputCV  [8]uint32
	block    [16]uint32
	counter  uint64
	blockLen uint32
	flags    uint32
}

func (o output) chainingValue() [8]uint32 {
	full := compress(o.inputCV, o.block, o.counter, o.blockLen, o.flags)
	var cv [8]uint32
	copy(cv[:], full[:8])
	return cv
}

// rootBytes returns the first n bytes of the root output (n <= 64).
func (o output) rootBytes() [outLen]byte {
	full := compress(o.inputCV, o.block, 0, o.blockLen, o.flags|flagRoot)
	var out [64]byte
	for i, w := range full {
		out[i*4+0] = byte(w)
		out[i*4+1] = byte(w >> 8)
		out[i*4+2] = byte(w >> 16)
		out[i*4+3] = byte(w >> 24)
	}
	var d [outLen]byte
	copy(d[:], out[:outLen])
	return d
}

func chunkCVOf(cv [8]uint32, m [16]uint32, counter uint64, blen, flags uint32) [8]uint32 {
	full := compress(cv, m, counter, blen, flags)
	var out [8]uint32
	copy(out[:], full[:8])
	return out
}

// parentOutput builds the parent node output for two child chaining values.
func parentOutput(left, right [8]uint32, key [8]uint32, flags uint32) output {
	var block [16]uint32
	copy(block[:8], left[:])
	copy(block[8:], right[:])
	return output{
		inputCV:  key,
		block:    block,
		counter:  0,
		blockLen: blockLen,
		flags:    flags | flagParent,
	}
}

// hashAll computes the BLAKE3 tree hash of input under key/flags.
func hashAll(input []byte, key [8]uint32, flags uint32) [outLen]byte {
	// cvStack holds the completed subtree chaining values, oldest first. The
	// maximum depth for any practically-hashable length is well under 54.
	var cvStack [64][8]uint32
	stackLen := 0

	chunks := (len(input) + chunkLen - 1) / chunkLen
	if chunks == 0 {
		chunks = 1
	}

	addCV := func(cv [8]uint32, totalChunks int) {
		for totalChunks&1 == 0 {
			stackLen--
			cv = parentOutput(cvStack[stackLen], cv, key, flags).chainingValue()
			totalChunks >>= 1
		}
		cvStack[stackLen] = cv
		stackLen++
	}

	for i := 0; i < chunks-1; i++ {
		c := chunkState{cv: key, counter: uint64(i), flags: flags}
		c.update(input[i*chunkLen : (i+1)*chunkLen])
		addCV(c.output().chainingValue(), i+1)
	}

	last := input[(chunks-1)*chunkLen:]
	lastChunk := chunkState{cv: key, counter: uint64(chunks - 1), flags: flags}
	lastChunk.update(last)
	out := lastChunk.output()

	for i := stackLen - 1; i >= 0; i-- {
		out = parentOutput(cvStack[i], out.chainingValue(), key, flags)
	}
	return out.rootBytes()
}
