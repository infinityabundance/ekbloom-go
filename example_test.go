package ekbloom_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	ekbloom "github.com/infinityabundance/ekbloom-go"
)

// Example_openFixture opens a `.ekb` file written by the Rust reference and reads
// a byte and a range by logical position.
func Example_openFixture() {
	data, _ := os.ReadFile(filepath.Join("testdata", "fixtures", "m1_root_text.ekb"))
	r, err := ekbloom.Open(data)
	if err != nil {
		fmt.Println("open:", err)
		return
	}
	b, _ := r.ByteAt(0)
	head, _ := r.ReadAt(0, 5)
	fmt.Printf("machine=%d n=%d physical=%d byte0=%q head=%q\n",
		r.MachineID(), r.LogicalLen(), r.PhysicalBytes(), b, head)
	// Output: machine=1 n=100 physical=100 byte0='t' head="the q"
}

// Example_dictionary trains one shared order-0 dictionary and encodes many small
// records against it — the machine-9 (OrANS-DICT) small-record path.
func Example_dictionary() {
	corpus := [][]byte{
		[]byte("user=alice|region=us-east-1|role=admin"),
		[]byte("user=bob|region=eu-west-1|role=reader"),
		[]byte("user=carol|region=us-west-2|role=writer"),
	}
	dict, err := ekbloom.TrainDictionary(corpus)
	if err != nil {
		fmt.Println("train:", err)
		return
	}
	record := []byte("user=dave|region=ap-south-1|role=admin")
	core, err := dict.EncodeRecord(record)
	if err != nil {
		fmt.Println("encode:", err)
		return
	}
	back, err := dict.DecodeRecord(core)
	if err != nil {
		fmt.Println("decode:", err)
		return
	}
	fmt.Printf("roundtrip=%v core=%d bytes raw=%d bytes\n", bytes.Equal(back, record), len(core), len(record))
	// Output: roundtrip=true core=36 bytes raw=38 bytes
}

// Example_writeBodyWithDict shows the machine-9 shared-dictionary path: one
// dictionary trained over a corpus, a record written as a compact core that
// *references* it, and read back through OpenWithDict.
func Example_writeBodyWithDict() {
	corpus := [][]byte{
		[]byte("user=alice|region=us-east-1|role=admin|status=active"),
		[]byte("user=bob|region=eu-west-1|role=reader|status=idle"),
		[]byte("user=carol|region=us-west-2|role=writer|status=active"),
	}
	dict, err := ekbloom.TrainDictionary(corpus)
	if err != nil {
		fmt.Println("train:", err)
		return
	}
	record := []byte("user=dave|region=ap-south-1|role=admin|status=active")
	image, err := ekbloom.WriteBodyWithDict(record, ekbloom.Profile{Kind: ekbloom.ProfileRoot}, dict)
	if err != nil {
		fmt.Println("write:", err)
		return
	}
	r, err := ekbloom.OpenWithDict(image, dict)
	if err != nil {
		fmt.Println("open:", err)
		return
	}
	got, err := r.ReadAt(0, uint64(len(record)))
	if err != nil {
		fmt.Println("read:", err)
		return
	}
	fmt.Printf("machine=%d roundtrip=%v\n", r.MachineID(), bytes.Equal(got, record))
	// Output: machine=9 roundtrip=true
}

// Example_streaming builds a memory-bounded stream container and seeks a range.
func Example_streaming() {
	data := make([]byte, 200000)
	for i := range data {
		data[i] = byte('a' + i%26)
	}
	var sink bytes.Buffer
	b, err := ekbloom.NewStreamBuilder(&sink, ekbloom.StreamOptions{BlockBytes: 1 << 14, Machine: ekbloom.PolicyAuto})
	if err != nil {
		fmt.Println("builder:", err)
		return
	}
	_ = b.Write(data)
	manifest, err := b.Finish()
	if err != nil {
		fmt.Println("finish:", err)
		return
	}
	r, err := ekbloom.OpenStream(sink.Bytes())
	if err != nil {
		fmt.Println("open:", err)
		return
	}
	buf := make([]byte, 16)
	_ = r.ReadAt(12345, buf)
	fmt.Printf("blocks=%d n=%d seek=%q\n", manifest.BlockCount, r.Len(), buf)
	// Output: blocks=13 n=200000 seek="vwxyzabcdefghijk"
}

// Example_corruption shows typed corruption handling: a single flipped bit is
// detected by Open and the deep verifier localizes it to a structure.
func Example_corruption() {
	data, _ := os.ReadFile(filepath.Join("testdata", "fixtures", "m1_root_allbytes.ekb"))
	corrupted := append([]byte(nil), data...)
	corrupted[80] ^= 0x01 // flip one bit inside the visible core
	if _, err := ekbloom.Open(corrupted); err != nil {
		section, _, ok := ekbloom.CorruptDetail(ekbloom.VerifyDeep(corrupted))
		fmt.Printf("detected=%v localized=%v section=%q\n", true, ok, section)
		return
	}
	fmt.Println("not detected")
	// Output: detected=true localized=true section="core"
}
