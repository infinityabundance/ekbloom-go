package ekb

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"
)

// TestDictDecoderMatchesDecodeRecord proves the reusable DictDecoder is
// byte-for-byte equivalent to Dictionary.DecodeRecord over deterministic
// text-like records of many lengths (including the empty record). A decoder
// built once decodes every record, and all three entry points agree.
func TestDictDecoderMatchesDecodeRecord(t *testing.T) {
	corpus := dictCorpus(256, 100)
	dict, err := TrainDictionary(corpus)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := NewDictDecoder(dict)
	if err != nil {
		t.Fatal(err)
	}

	sizes := []int{0, 1, 2, 3, 4, 7, 8, 15, 16, 31, 38, 63, 100, 127, 255, 256, 257, 511, 512, 1000}
	records := make([][]byte, 0, len(sizes)*3)
	for _, n := range sizes {
		records = append(records, makeDictRecord(n*7+1, n))
		records = append(records, makeDictRecord(n*13+5, n))
		records = append(records, makeDictRecord(n*31+9, n))
	}

	for i, rec := range records {
		core, err := dict.EncodeRecord(rec)
		if err != nil {
			t.Fatalf("record %d (%d B) encode: %v", i, len(rec), err)
		}
		want, err := dict.DecodeRecord(core)
		if err != nil {
			t.Fatalf("record %d DecodeRecord: %v", i, err)
		}
		if !bytes.Equal(want, rec) {
			t.Fatalf("record %d: DecodeRecord output != source", i)
		}

		got, err := dec.Decode(core)
		if err != nil {
			t.Fatalf("record %d Decode: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("record %d: Decode != DecodeRecord", i)
		}

		dst := make([]byte, len(rec)+8)
		n, err := dec.DecodeInto(dst, core)
		if err != nil {
			t.Fatalf("record %d DecodeInto: %v", i, err)
		}
		if n != len(want) || !bytes.Equal(dst[:n], want) {
			t.Fatalf("record %d: DecodeInto (n=%d) != DecodeRecord", i, n)
		}
	}
}

// TestDictDecoderFixtures runs Decode/DecodeInto against the frozen Rust machine-9
// fixtures (reference and embedded dictionaries) and requires each to reproduce
// the fixture source and to match Dictionary.DecodeRecord byte-for-byte.
func TestDictDecoderFixtures(t *testing.T) {
	fix := loadFixtures(t)
	tested := 0
	for _, fx := range fix.Fixtures {
		if fx.MachineID != MachineDict {
			continue
		}
		fx := fx
		t.Run(fx.Name, func(t *testing.T) {
			if fx.Dict != "" {
				db, err := os.ReadFile(testdataPath(fx.Dict))
				if err != nil {
					t.Fatalf("read dict: %v", err)
				}
				d, err := DictionaryFromBytes(db)
				if err != nil {
					t.Fatalf("parse dict: %v", err)
				}
				RegisterDictionary(d)
			}
			image, err := os.ReadFile(testdataPath("fixtures", fx.Name+".ekb"))
			if err != nil {
				t.Fatalf("read ekb: %v", err)
			}
			src, err := os.ReadFile(testdataPath("fixtures", fx.Name+".src"))
			if err != nil {
				t.Fatalf("read src: %v", err)
			}
			r, err := Open(image)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			framed := r.CoreBytes()
			if len(framed) < dictFramePrefixBytes {
				t.Fatalf("framed core too short: %d", len(framed))
			}
			dictLen := int(binary.LittleEndian.Uint32(framed[16:20]))

			var d Dictionary
			if fx.Dict != "" {
				db, err := os.ReadFile(testdataPath(fx.Dict))
				if err != nil {
					t.Fatalf("read dict: %v", err)
				}
				if d, err = DictionaryFromBytes(db); err != nil {
					t.Fatalf("parse dict: %v", err)
				}
			} else {
				if dictFramePrefixBytes+dictLen > len(framed) {
					t.Fatalf("embedded dictionary truncated: dictLen=%d framed=%d", dictLen, len(framed))
				}
				if d, err = DictionaryFromBytes(framed[dictFramePrefixBytes : dictFramePrefixBytes+dictLen]); err != nil {
					t.Fatalf("parse embedded dict: %v", err)
				}
			}
			core := framed[dictFramePrefixBytes+dictLen:]

			dec, err := NewDictDecoder(d)
			if err != nil {
				t.Fatalf("new decoder: %v", err)
			}
			want, err := d.DecodeRecord(core)
			if err != nil {
				t.Fatalf("DecodeRecord: %v", err)
			}
			if !bytes.Equal(want, src) {
				t.Fatalf("DecodeRecord output != fixture source")
			}
			got, err := dec.Decode(core)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("Decode != DecodeRecord")
			}
			dst := make([]byte, len(want))
			n, err := dec.DecodeInto(dst, core)
			if err != nil {
				t.Fatalf("DecodeInto: %v", err)
			}
			if n != len(want) || !bytes.Equal(dst[:n], want) {
				t.Fatalf("DecodeInto (n=%d) != DecodeRecord", n)
			}
			tested++
		})
	}
	if tested == 0 {
		t.Fatal("no machine-9 fixtures found")
	}
}

// TestDictDecoderDecodeIntoZeroAllocs proves that a decode into a reused buffer
// performs 0 allocations on the steady-state path — the "8 -> 2" reduction: the
// 516-byte model header, the rANS context, and the per-record output slice are
// all eliminated.
func TestDictDecoderDecodeIntoZeroAllocs(t *testing.T) {
	corpus := dictCorpus(128, 100)
	dict, err := TrainDictionary(corpus)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := NewDictDecoder(dict)
	if err != nil {
		t.Fatal(err)
	}
	rec := makeDictRecord(4242, 100)
	core, err := dict.EncodeRecord(rec)
	if err != nil {
		t.Fatal(err)
	}
	dst := make([]byte, len(rec))

	avg := testing.AllocsPerRun(500, func() {
		n, err := dec.DecodeInto(dst, core)
		if err != nil || n != len(rec) {
			t.Fatalf("DecodeInto: n=%d err=%v", n, err)
		}
	})
	if avg != 0 {
		t.Fatalf("DecodeInto allocs/op = %v, want 0", avg)
	}

	// The allocating convenience form is still correct.
	got, err := dec.Decode(core)
	if err != nil || !bytes.Equal(got, rec) {
		t.Fatalf("Decode round-trip failed: %v", err)
	}
}

// TestDictDecoderErrors pins the typed-error behaviour of the reusable decoder:
// an undersized destination, a truncated core, an out-of-range state and an
// invalid dictionary are all refused, never a wrong-length or out-of-bounds
// decode.
func TestDictDecoderErrors(t *testing.T) {
	corpus := dictCorpus(32, 64)
	dict, err := TrainDictionary(corpus)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := NewDictDecoder(dict)
	if err != nil {
		t.Fatal(err)
	}
	core, err := dict.EncodeRecord([]byte("some record content for the decoder"))
	if err != nil {
		t.Fatal(err)
	}
	logicalLen := int(binary.LittleEndian.Uint32(core[0:]))
	if logicalLen <= 1 {
		t.Fatalf("unexpected logical length %d", logicalLen)
	}

	if _, err := dec.DecodeInto(make([]byte, logicalLen-1), core); err == nil {
		t.Fatal("undersized destination accepted")
	}
	if _, err := dec.DecodeInto(make([]byte, logicalLen), core[:4]); err == nil {
		t.Fatal("truncated core accepted by DecodeInto")
	}
	if _, err := dec.Decode(core[:dictRecordHeaderBytes-1]); err == nil {
		t.Fatal("truncated core accepted by Decode")
	}
	bad := append([]byte(nil), core...)
	binary.LittleEndian.PutUint32(bad[4:], 0)
	if _, err := dec.DecodeInto(make([]byte, logicalLen), bad); err == nil {
		t.Fatal("out-of-range state accepted")
	}
	if n, err := dec.DecodeInto(make([]byte, logicalLen), core); err != nil || n != logicalLen {
		t.Fatalf("exact-size destination: n=%d err=%v", n, err)
	}

	// A never-trained dictionary has no valid model.
	var zero Dictionary
	if _, err := NewDictDecoder(zero); err == nil {
		t.Fatal("zero dictionary accepted")
	}
}
