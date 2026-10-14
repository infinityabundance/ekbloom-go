// Command bridge is a cgo c-shared library that exposes the EKBLOOM machine-9
// shared-dictionary value codec to a host-language consumer (here the RocksDB
// C++ program in ../rocksdb_blob.cc).
//
// It is a *separate Go module* (see go.mod, which replaces the core module with
// ../../../) so the pure-Go core stays free of cgo and unsafe. Build it with:
//
//	go build -buildmode=c-shared -o libekbloom.so .
//
// which also emits libekbloom.h. The exported C ABI is intentionally tiny and
// lives entirely in this file:
//
//	int  ekbloom_train(const unsigned char *corpus, int n, unsigned char *idOut);
//	int  ekbloom_compress(const unsigned char *src, int n, unsigned char *dst, int cap);
//	int  ekbloom_decompress(const unsigned char *src, int n, unsigned char *dst, int cap);
//	int  ekbloom_dict(unsigned char *dst, int cap);
//	int  ekbloom_dict_bytes(void);
//	long ekbloom_breaches(void);
//
// There is exactly one trained Dictionary, guarded by one mutex. Train once
// (single-writer) and then call compress/decompress from any thread; the codec
// itself is read-only after training.
//
// Corpus format for ekbloom_train: a flat buffer of n bytes holding consecutive
// length-prefixed records, each `uint32 little-endian length || length bytes`.
// This keeps the ABI to a single (pointer, length) pair with no struct layout
// assumptions. ekbloom_train returns 0 on success and writes the 16-byte
// dictionary content id into idOut.
package main

/*
#include <stdlib.h>
*/
import "C"

import (
	"encoding/binary"
	"errors"
	"sync"
	"unsafe"

	ekbloom "github.com/infinityabundance/ekbloom-go"
)

var (
	mu    sync.RWMutex
	dict  ekbloom.Dictionary
	ready bool
)

// dictIDBytes is the width of a serialized dictionary id (16 bytes).
const dictIDBytes = 16

var errBadFrame = errors.New("ekbloom bridge: malformed length-prefixed corpus")

// parseFrames splits a length-prefixed corpus into records. Each frame is a
// uint32 little-endian length followed by that many bytes.
func parseFrames(buf []byte) ([][]byte, error) {
	var recs [][]byte
	for off := 0; off < len(buf); {
		if off+4 > len(buf) {
			return nil, errBadFrame
		}
		ln := int(binary.LittleEndian.Uint32(buf[off:]))
		off += 4
		if ln < 0 || off+ln > len(buf) {
			return nil, errBadFrame
		}
		recs = append(recs, buf[off:off+ln])
		off += ln
	}
	return recs, nil
}

// ekbloom_train trains the single shared dictionary from a length-prefixed
// corpus and writes its 16-byte id to idOut. Returns 0 on success, negative on
// error.
//
//export ekbloom_train
func ekbloom_train(corpus *C.uchar, n C.int, idOut *C.uchar) C.int {
	if corpus == nil || n <= 0 {
		return -1
	}
	raw := C.GoBytes(unsafe.Pointer(corpus), n)
	recs, err := parseFrames(raw)
	if err != nil || len(recs) == 0 {
		return -2
	}
	d, err := ekbloom.TrainDictionary(recs)
	if err != nil {
		return -3
	}
	mu.Lock()
	dict, ready = d, true
	mu.Unlock()
	if idOut != nil {
		id := d.ID()
		out := unsafe.Slice((*byte)(unsafe.Pointer(idOut)), dictIDBytes)
		copy(out, id[:])
	}
	return 0
}

// ekbloom_compress encodes src (n bytes) into dst (cap bytes). Returns the
// number of bytes written, or a negative value on error (including "cap too
// small").
//
//export ekbloom_compress
func ekbloom_compress(src *C.uchar, n C.int, dst *C.uchar, cap C.int) C.int {
	if src == nil || dst == nil || n < 0 || cap <= 0 {
		return -1
	}
	mu.RLock()
	d, ok := dict, ready
	mu.RUnlock()
	if !ok {
		return -2
	}
	in := C.GoBytes(unsafe.Pointer(src), n)
	core, err := d.EncodeRecord(in)
	if err != nil {
		return -3
	}
	if len(core) > int(cap) {
		return -4
	}
	out := unsafe.Slice((*byte)(unsafe.Pointer(dst)), int(cap))
	copy(out, core)
	return C.int(len(core))
}

// ekbloom_decompress decodes src (n bytes) into dst (cap bytes). Returns the
// number of logical bytes written, or a negative value on error.
//
//export ekbloom_decompress
func ekbloom_decompress(src *C.uchar, n C.int, dst *C.uchar, cap C.int) C.int {
	if src == nil || dst == nil || n < 0 || cap <= 0 {
		return -1
	}
	mu.RLock()
	d, ok := dict, ready
	mu.RUnlock()
	if !ok {
		return -2
	}
	in := C.GoBytes(unsafe.Pointer(src), n)
	decoded, err := d.DecodeRecord(in)
	if err != nil {
		return -3
	}
	if len(decoded) > int(cap) {
		return -4
	}
	out := unsafe.Slice((*byte)(unsafe.Pointer(dst)), int(cap))
	copy(out, decoded)
	return C.int(len(decoded))
}

// ekbloom_dict serializes the trained dictionary into dst (cap bytes). Returns
// the number of bytes written, or a negative value on error. A consumer stores
// this alongside the values so the dictionary survives a restart.
//
//export ekbloom_dict
func ekbloom_dict(dst *C.uchar, cap C.int) C.int {
	if dst == nil || cap <= 0 {
		return -1
	}
	mu.RLock()
	d, ok := dict, ready
	mu.RUnlock()
	if !ok {
		return -2
	}
	b := d.Bytes()
	if len(b) > int(cap) {
		return -3
	}
	out := unsafe.Slice((*byte)(unsafe.Pointer(dst)), int(cap))
	copy(out, b)
	return C.int(len(b))
}

// ekbloom_dict_bytes returns the serialized dictionary size (540 bytes for a
// valid machine-9 dictionary), or 0 if untrained.
//
//export ekbloom_dict_bytes
func ekbloom_dict_bytes() C.int {
	mu.RLock()
	d, ok := dict, ready
	mu.RUnlock()
	if !ok {
		return 0
	}
	return C.int(d.PhysicalBytes())
}

// ekbloom_breaches reports the process-wide resource-policy breach counter: the
// number of whole-object allocations the 1 GiB default cap has refused. Exposed
// so the value-codec host can export the same operational metric the core does.
//
//export ekbloom_breaches
func ekbloom_breaches() C.long {
	return C.long(ekbloom.ResourcePolicySnapshot().AllocationBreaches)
}

func main() {}
