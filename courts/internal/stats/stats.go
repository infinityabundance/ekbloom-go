// Package stats provides the median-of-runs latency statistic used by the courts
// harnesses (a separate Go module, so it cannot import the root module's
// internal/tools/stat). It is a verbatim copy of that package's API.
//
// A measurement is repeated R times; each run yields one value per metric (a
// quantile of that run's samples, or a per-run scalar such as MB/s), and the value
// reported in the evidence is the MEDIAN across runs, with the observed min and max
// kept so a reviewer can see the run-to-run spread.
//
// R = EKB_RUNS, default 5 (or 3 when CI is set, so CI stays time-bounded); odd
// values are preferred so the median is an observed run. See Runs.
package stats

import (
	"cmp"
	"os"
	"sort"
	"strconv"
	"time"
)

// Runs reports the number of measurement runs R.
//
//	R = EKB_RUNS   if set to a positive integer
//	R = 3          if CI is set (CI must stay time-bounded)
//	R = 5          otherwise
//
// Values below 1 clamp to 1. Odd values are preferred so the median is an observed
// run.
func Runs() int {
	def := 5
	if os.Getenv("CI") != "" {
		def = 3
	}
	if v := os.Getenv("EKB_RUNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// Stats is the median-of-runs statistic for one metric: the median across the runs,
// plus the observed minimum and maximum, over N runs.
type Stats[T cmp.Ordered] struct {
	Median T
	Min    T
	Max    T
	N      int
}

// Collect reduces the per-run observations xs to their median, minimum and maximum.
// For an even number of runs the median is the upper of the two middle values (no
// interpolation), so the reported value is always an observed run.
func Collect[T cmp.Ordered](xs []T) Stats[T] {
	if len(xs) == 0 {
		var zero T
		return Stats[T]{Median: zero, Min: zero, Max: zero}
	}
	s := append([]T(nil), xs...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return Stats[T]{Median: s[len(s)/2], Min: s[0], Max: s[len(s)-1], N: len(s)}
}

// Repeat calls fn once per run, with run in [0, Runs()). It is the single place the
// run count is turned into iterations.
func Repeat(fn func(run int)) {
	for r := 0; r < Runs(); r++ {
		fn(r)
	}
}

// Sample runs sample R = Runs() times; sample(run) returns one run's observations.
// For each requested per-mille quantile (e.g. 500, 990, 999) it returns the
// median-of-runs statistic of that quantile across runs, in nanoseconds. The
// returned slice is in the same order as perMille.
func Sample(sample func(run int) []time.Duration, perMille ...int) []Stats[int64] {
	runs := Runs()
	per := make([][]int64, len(perMille))
	for r := 0; r < runs; r++ {
		d := sample(r)
		sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
		for k, pm := range perMille {
			per[k] = append(per[k], Pct(d, pm))
		}
	}
	out := make([]Stats[int64], len(perMille))
	for k := range perMille {
		out[k] = Collect(per[k])
	}
	return out
}

// Pct returns the perMille-th percentile (0..1000) of the sorted observations d, in
// nanoseconds. It matches the convention used throughout the harnesses: index
// len(d)*perMille/1000 with no interpolation.
func Pct(d []time.Duration, perMille int) int64 {
	if len(d) == 0 {
		return 0
	}
	i := len(d) * perMille / 1000
	if i >= len(d) {
		i = len(d) - 1
	}
	return d[i].Nanoseconds()
}
