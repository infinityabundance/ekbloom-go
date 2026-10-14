package ekb

import (
	"encoding/binary"
	"sort"
	"sync"

	"github.com/infinityabundance/ekbloom-go/internal/blake3"
)

// Dictionary wire constants (machine 9).
var dictMagic = [8]byte{'E', 'K', 'B', 'D', 'I', 'C', 'T', 0}

const (
	dictVersion           = 1
	dictHeaderBytes       = 8 + 2 + 2 + 16                   // 28
	dictModelBytes        = ransSymbols * 2                  // 512
	dictBytes             = dictHeaderBytes + dictModelBytes // 540
	dictRecordHeaderBytes = 8
	dictFramePrefixBytes  = 20
)

var dictIDDomain = []byte("ekbloom.dictionary-id.v1")

// scaleCounts scales aggregated byte counts to a 4096 frequency table by the
// largest-remainder method (each symbol reserved 1, the rest proportional).
func scaleCounts(count *[ransSymbols]uint64) [ransSymbols]uint16 {
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
	const budget = uint64(ransM) - ransSymbols
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
	var freq [ransSymbols]uint16
	for i := 0; i < ransSymbols; i++ {
		freq[i] = uint16(rem[i] + 1)
	}
	return freq
}

var dictRegistry = struct {
	mu sync.RWMutex
	m  map[[16]byte]Dictionary
}{m: map[[16]byte]Dictionary{}}

// Dictionary is a trained shared order-0 model referenced by many small records.
type Dictionary struct {
	id   [16]byte
	freq [ransSymbols]uint16
}

func dictComputeID(freq *[ransSymbols]uint16) [16]byte {
	buf := make([]byte, 0, len(dictIDDomain)+2+ransModelBytes)
	buf = append(buf, dictIDDomain...)
	buf = appendU16(buf, dictVersion)
	for i := 0; i < ransSymbols; i++ {
		buf = appendU16(buf, freq[i])
	}
	sum := blake3.Sum(buf)
	return [16]byte(sum[:16])
}

// TrainDictionary trains a shared order-0 dictionary from a corpus of records.
func TrainDictionary(records [][]byte) (Dictionary, error) {
	var d Dictionary
	var count [ransSymbols]uint64
	for _, rec := range records {
		for _, b := range rec {
			count[b]++
			if count[b] == 0 {
				return d, overflow("dictionary byte count")
			}
		}
	}
	d.freq = scaleCounts(&count)
	d.id = dictComputeID(&d.freq)
	return d, nil
}

// ID returns the dictionary's content identity.
func (d Dictionary) ID() [16]byte { return d.id }

// PhysicalBytes is the serialized size of the dictionary (540 bytes).
func (d Dictionary) PhysicalBytes() uint64 { return dictBytes }

// Bytes serializes the dictionary.
func (d Dictionary) Bytes() []byte {
	out := make([]byte, 0, dictBytes)
	out = append(out, dictMagic[:]...)
	out = appendU16(out, dictVersion)
	out = appendU16(out, 0)
	out = append(out, d.id[:]...)
	for i := 0; i < ransSymbols; i++ {
		out = appendU16(out, d.freq[i])
	}
	return out
}

// DictionaryFromBytes parses and validates a serialized dictionary.
func DictionaryFromBytes(b []byte) (Dictionary, error) {
	var d Dictionary
	if len(b) < dictBytes {
		return d, truncated(dictBytes, uint64(len(b)))
	}
	if len(b) > dictBytes {
		return d, badSection("dictionary record has trailing bytes")
	}
	if [8]byte(b[0:8]) != dictMagic {
		return d, badSection("bad dictionary magic")
	}
	if binary.LittleEndian.Uint16(b[8:]) != dictVersion {
		return d, badSection("unsupported dictionary version")
	}
	copy(d.id[:], b[12:28])
	var sum uint32
	for i := 0; i < ransSymbols; i++ {
		v := binary.LittleEndian.Uint16(b[dictHeaderBytes+i*2:])
		d.freq[i] = v
		sum += uint32(v)
		if v == 0 {
			return d, badSection("dictionary contains a zero frequency")
		}
	}
	if sum != ransM {
		return d, badSection("dictionary frequencies do not sum to 4096")
	}
	if dictComputeID(&d.freq) != d.id {
		return d, badSection("dictionary content id mismatch")
	}
	return d, nil
}

// RegisterDictionary installs d so a machine-9 core may reference it by id.
func RegisterDictionary(d Dictionary) {
	dictRegistry.mu.Lock()
	dictRegistry.m[d.id] = d
	dictRegistry.mu.Unlock()
}

// LookupDictionary resolves a registered dictionary by content id.
func LookupDictionary(id [16]byte) (Dictionary, bool) {
	dictRegistry.mu.RLock()
	d, ok := dictRegistry.m[id]
	dictRegistry.mu.RUnlock()
	return d, ok
}

func dictCumulate(freq *[ransSymbols]uint16) [ransSymbols]uint32 {
	var cum [ransSymbols]uint32
	var acc uint32
	for i := 0; i < ransSymbols; i++ {
		cum[i] = acc
		acc += uint32(freq[i])
	}
	return cum
}

// EncodeRecord encodes record as a compact header-less core referencing d.
func (d Dictionary) EncodeRecord(record []byte) ([]byte, error) {
	if uint64(len(record)) > uint64(^uint32(0)) {
		return nil, overflow("dictionary record length")
	}
	cum := dictCumulate(&d.freq)
	x := ransL
	emitted := make([]byte, 0, len(record))
	for i := len(record) - 1; i >= 0; i-- {
		sym := record[i]
		f := uint64(d.freq[sym])
		c := uint64(cum[sym])
		xmax := uint64(ransL>>scaleBits<<8) * f
		for uint64(x) >= xmax {
			emitted = append(emitted, byte(x))
			x >>= 8
		}
		xu := uint64(x)
		x = uint32((xu/f)<<scaleBits + xu%f + c)
	}
	out := make([]byte, 0, dictRecordHeaderBytes+len(emitted))
	out = appendU32(out, uint32(len(record)))
	out = appendU32(out, x)
	for i := len(emitted) - 1; i >= 0; i-- {
		out = append(out, emitted[i])
	}
	return out, nil
}

func (d Dictionary) modelHeader(initialState uint32) []byte {
	h := make([]byte, ransModelBytes+4)
	for i := 0; i < ransSymbols; i++ {
		binary.LittleEndian.PutUint16(h[i*2:], d.freq[i])
	}
	binary.LittleEndian.PutUint32(h[ransModelBytes:], initialState)
	return h
}

// DecodeRecord decodes a core produced by EncodeRecord against d.
func (d Dictionary) DecodeRecord(core []byte) ([]byte, error) {
	if len(core) < dictRecordHeaderBytes {
		return nil, truncated(dictRecordHeaderBytes, uint64(len(core)))
	}
	logicalLen := uint64(binary.LittleEndian.Uint32(core[0:]))
	state := binary.LittleEndian.Uint32(core[4:])
	if !validState(state) {
		return nil, badSection("dictionary record state out of range")
	}
	header := d.modelHeader(state)
	ctx, err := ransContextFromCore(header)
	if err != nil {
		return nil, err
	}
	var s Snapshot
	s.State = state
	s.Cursor = dictRecordHeaderBytes
	out := make([]byte, logicalLen)
	for i := uint64(0); i < logicalLen; i++ {
		b, err := ctx.reveal(core, &s)
		if err != nil {
			return nil, err
		}
		out[i] = b
	}
	return out, nil
}

func frameReference(d Dictionary, core []byte) []byte {
	out := make([]byte, 0, dictFramePrefixBytes+len(core))
	out = append(out, d.id[:]...)
	out = appendU32(out, 0)
	out = append(out, core...)
	return out
}

func frameEmbedded(d Dictionary, core []byte) []byte {
	db := d.Bytes()
	out := make([]byte, 0, dictFramePrefixBytes+len(db)+len(core))
	out = append(out, d.id[:]...)
	out = appendU32(out, uint32(len(db)))
	out = append(out, db...)
	out = append(out, core...)
	return out
}
