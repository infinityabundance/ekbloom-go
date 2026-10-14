// Command fingerprint prints a deterministic fingerprint of every `.ekb`/`.stream`
// file in a directory: machine id, logical length, physical bytes, and a SHA-256
// over the fully decoded logical bytes. Used to prove byte-for-byte determinism
// across filesystems and architectures.
//
// Usage: go run ./internal/tools/fingerprint <dir>
package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	ekbloom "github.com/infinityabundance/ekbloom-go"
)

func testKey() [32]byte {
	var k [32]byte
	for i := range k {
		k[i] = byte(i)
	}
	return k
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: fingerprint <dir>")
		os.Exit(2)
	}
	dir := os.Args[1]
	dicts, _ := filepath.Glob(filepath.Join(dir, "*.dict"))
	dicts2, _ := filepath.Glob(filepath.Join(dir, "*", "*.dict"))
	dicts = append(dicts, dicts2...)
	for _, dp := range dicts {
		if b, err := os.ReadFile(dp); err == nil {
			if d, err := ekbloom.DictionaryFromBytes(b); err == nil {
				ekbloom.RegisterDictionary(d)
			}
		}
	}
	var files []string
	for _, pat := range []string{"*.ekb", "*/*.ekb", "*.stream", "*/*.stream"} {
		m, _ := filepath.Glob(filepath.Join(dir, pat))
		files = append(files, m...)
	}
	sort.Strings(files)
	for _, f := range files {
		fp(filepath.Base(f), f)
	}
}

func fp(name, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("%s ERROR %v\n", name, err)
		return
	}
	if filepath.Ext(path) == ".stream" {
		r, err := ekbloom.OpenStream(data)
		if err != nil {
			fmt.Printf("%s STREAM-ERROR %v\n", name, err)
			return
		}
		buf := make([]byte, r.Len())
		if err := r.ReadAt(0, buf); err != nil {
			fmt.Printf("%s STREAM-READ-ERROR %v\n", name, err)
			return
		}
		sum := sha256.Sum256(buf)
		fmt.Printf("%s stream n=%d sha=%x\n", name, r.Len(), sum)
		return
	}
	r, err := ekbloom.Open(data)
	if err != nil {
		r2, err2 := ekbloom.OpenAuthenticated(data, testKey())
		if err2 != nil {
			fmt.Printf("%s ERROR %v\n", name, err)
			return
		}
		r = r2
	}
	buf, err := r.ReadAt(0, r.LogicalLen())
	if err != nil {
		fmt.Printf("%s READ-ERROR %v\n", name, err)
		return
	}
	sum := sha256.Sum256(buf)
	fmt.Printf("%s machine=%d n=%d physical=%d sha=%x\n", name, r.MachineID(), r.LogicalLen(), r.PhysicalBytes(), sum)
}
