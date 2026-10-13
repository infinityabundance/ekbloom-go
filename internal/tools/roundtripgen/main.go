// Command roundtripgen writes `.ekb` files produced by the Go implementation so
// the Rust reference can read them back (proving bidirectional format identity).
//
// Usage: go run ./internal/tools/roundtripgen <output-dir>
package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	ekbloom "github.com/infinityabundance/ekbloom-go"
)

func lcg(seed, n int) []byte {
	s := uint64(seed)
	out := make([]byte, n)
	for i := range out {
		s = s*6364136223846793005 + 1442695040888963407
		out[i] = byte(s >> 33)
	}
	return out
}

func skewed(seed, n int) []byte {
	s := uint64(seed)
	alpha := []byte("abcd")
	out := make([]byte, n)
	for i := range out {
		s = s*6364136223846793005 + 1442695040888963407
		r := (s >> 33) % 100
		var idx int
		switch {
		case r < 60:
			idx = 0
		case r < 85:
			idx = 1
		case r < 95:
			idx = 2
		default:
			idx = 3
		}
		out[i] = alpha[idx]
	}
	return out
}

func text(n int) []byte {
	base := []byte("the quick brown fox jumps over 0123456789 lazy dog; ")
	out := make([]byte, 0, n)
	for len(out) < n {
		out = append(out, base...)
	}
	return out[:n]
}

func seq(n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(i)
	}
	return out
}

func mixed(n int) []byte {
	rnd := lcg(0xABCDEF, n)
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		if (i/512)%3 == 0 {
			out[i] = 0x7F
		} else {
			out[i] = rnd[i]
		}
	}
	return out
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: roundtripgen <output-dir>")
		os.Exit(2)
	}
	dir := os.Args[1]
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	type item struct {
		name      string
		machineID uint32
		profile   ekbloom.Profile
		source    []byte
	}
	profRoot := ekbloom.Profile{Kind: ekbloom.ProfileRoot}
	profFull := ekbloom.Profile{Kind: ekbloom.ProfileFull}
	profSparse := ekbloom.Profile{Kind: ekbloom.ProfileSparse, Interval: 64}

	items := []item{
		{"go_m1_root_text", 1, profRoot, text(100)},
		{"go_m1_root_allbytes", 1, profRoot, seq(256)},
		{"go_m1_root_empty", 1, profRoot, nil},
		{"go_m1_full_rand", 1, profFull, lcg(11, 2048)},
		{"go_m1_sparse64_text", 1, profSparse, text(1000)},
		{"go_m2_root_skew", 2, profRoot, skewed(12, 5000)},
		{"go_m2_full_skew", 2, profFull, skewed(13, 3000)},
		{"go_m3_root_rand", 3, profRoot, lcg(14, 4096)},
		{"go_m4_root_skew", 4, profRoot, skewed(15, 5000)},
		{"go_m5_root_skew", 5, profRoot, skewed(16, 5000)},
		{"go_m6_root_mixed", 6, profRoot, mixed(8192)},
		{"go_m7_root_skew", 7, profRoot, skewed(17, 5000)},
		{"go_m8_root_skew", 8, profRoot, skewed(18, 5000)},
		{"go_m9_root_small", 9, profRoot, []byte("user=alice|region=us-east-1|role=admin")},
		{"go_m9_full_small", 9, profFull, []byte("user=alice|region=us-east-1|role=admin")},
	}

	fail := 0
	for _, it := range items {
		ekb, err := ekbloom.WriteBody(it.machineID, it.source, it.profile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: write: %v\n", it.name, err)
			fail++
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, it.name+".ekb"), ekb, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := os.WriteFile(filepath.Join(dir, it.name+".src"), it.source, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("wrote %-22s machine=%d profile=%d bytes=%d\n", it.name, it.machineID, it.profile.Kind, len(ekb))
	}
	if fail > 0 {
		os.Exit(1)
	}

	// A Go-written stream container, verified by the Rust StreamReader.
	streamSrc := make([]byte, 30000)
	for i := range streamSrc {
		streamSrc[i] = byte('a' + i%7)
	}
	var sink bytes.Buffer
	builder, err := ekbloom.NewStreamBuilder(&sink, ekbloom.StreamOptions{BlockBytes: 4096, Machine: ekbloom.PolicyAuto})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := builder.Write(streamSrc[:9000]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := builder.Checkpoint(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := builder.Write(streamSrc[9000:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	manifest, err := builder.Finish()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(filepath.Join(dir, "go_stream.stream"), sink.Bytes(), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(filepath.Join(dir, "go_stream.src"), streamSrc, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %-22s stream blocks=%d logical=%d\n", "go_stream", manifest.BlockCount, manifest.LogicalLen)
}
