// Package workload holds the deterministic record generator and the 80/20 key
// skew shared by the Pebble courts (courts/pebble, courts/pebblebench).
//
// It is deliberately tiny and pure: every court must produce byte-identical
// records and access traces so their numbers are comparable, which is why the
// generator lives here rather than being copied into each court.
package workload

// RecordBytes is the fixed logical size of every generated record.
const RecordBytes = 100

// HotFrac is the 80/20 skew denominator: 1/HotFrac of the keys is "hot".
const HotFrac = 5

// Tokens is the skewed/repetitive token alphabet the courts share, so every
// court measures the same byte distribution.
var Tokens = []string{"id=", "user", "kind=", "alpha", "beta", "gamma", "region=",
	"us-east-1", "eu-west-1", "ap-south-1", "score=", "000", "111", "222",
	"status=", "active", "idle", "|"}

// Record builds one text-like record of exactly RecordBytes bytes.
func Record(seed int) []byte {
	out := make([]byte, 0, RecordBytes+16)
	k := uint64(seed)
	for len(out) < RecordBytes {
		k = k*6364136223846793005 + 1442695040888963407
		out = append(out, Tokens[(k>>33)%uint64(len(Tokens))]...)
	}
	return out[:RecordBytes]
}

// SkewIndex picks a key index with an 80/20 skew: 80% of accesses target the
// first 1/HotFrac of the keys.
func SkewIndex(s *uint64, n int) int {
	hot := n / HotFrac
	if hot < 1 {
		hot = 1
	}
	*s = *s*6364136223846793005 + 1442695040888963407
	if (*s>>33)%100 < 80 {
		*s = *s*6364136223846793005 + 1442695040888963407
		return int((*s >> 20) % uint64(hot))
	}
	*s = *s*6364136223846793005 + 1442695040888963407
	return int((*s >> 20) % uint64(n))
}
