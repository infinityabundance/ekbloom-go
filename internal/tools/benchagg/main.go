// Command benchagg reduces `go test -bench` output (generated with -count=R) to
// median-of-runs statistics: for each benchmark it reports the median across the
// R runs of every measured metric (`ns/op`, `MB/s`, `B/op`, `allocs/op`), with the
// observed minimum and maximum, so a single noisy run cannot move the recorded
// numbers.
//
// Usage:
//
//	go test -run '^$' -bench . -benchmem -count=$(go run ./internal/tools/benchagg -print-runs) ./... \
//	    | go run ./internal/tools/benchagg > evidence/bench.txt
//
// The output is a tab-separated table (one row per benchmark), preceded by
// `#`-prefixed header lines — so evidence/bench.txt stays both human- and
// machine-readable (field 1 is the benchmark name, field 3 is the median ns/op).
//
// R = EKB_RUNS (default 5; 3 in CI), read through internal/tools/stat so the whole
// evidence set is regenerated at the same run count.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/infinityabundance/ekbloom-go/internal/tools/stat"
)

func main() {
	printRuns := flag.Bool("print-runs", false, "print R = EKB_RUNS and exit (for -count=R)")
	flag.Parse()

	if *printRuns {
		fmt.Println(stat.Runs())
		return
	}

	agg := newAggregator()
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		agg.add(sc.Text())
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "benchagg: read:", err)
		os.Exit(1)
	}
	if len(agg.benches) == 0 {
		fmt.Fprintln(os.Stderr, "benchagg: no benchmark lines on stdin")
		os.Exit(1)
	}
	agg.write(os.Stdout)
}

// bench is the set of per-run observations for one benchmark name (the name keeps
// the "-GOMAXPROCS" suffix go test prints).
type bench struct {
	name    string
	iters   []int64
	metrics map[string][]float64 // unit (e.g. "ns/op") -> one value per run
}

// aggregator collects benchmark lines across all R runs.
type aggregator struct {
	benches []*bench
	index   map[string]*bench
	units   []string // global metric order, first seen
	unitSet map[string]bool
}

func newAggregator() *aggregator {
	return &aggregator{index: map[string]*bench{}, unitSet: map[string]bool{}}
}

// add parses one line of `go test -bench` output. Non-benchmark lines are ignored.
// A benchmark line is "Name-N  iters  value unit  value unit ...".
func (a *aggregator) add(line string) {
	fields := strings.Fields(strings.TrimRight(line, "\r\n"))
	if len(fields) < 3 || !strings.HasPrefix(fields[0], "Benchmark") {
		return
	}
	iters, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return
	}
	b := a.index[fields[0]]
	if b == nil {
		b = &bench{name: fields[0], metrics: map[string][]float64{}}
		a.index[fields[0]] = b
		a.benches = append(a.benches, b)
	}
	b.iters = append(b.iters, iters)
	for i := 2; i+1 < len(fields); i += 2 {
		v, err := strconv.ParseFloat(fields[i], 64)
		if err != nil {
			continue
		}
		unit := fields[i+1]
		b.metrics[unit] = append(b.metrics[unit], v)
		if !a.unitSet[unit] {
			a.unitSet[unit] = true
			a.units = append(a.units, unit)
		}
	}
}

// orderedUnits returns the metric units in a canonical order (ns/op, MB/s, B/op,
// allocs/op first), so evidence/bench.txt has stable columns and ns/op is always
// field 3.
func (a *aggregator) orderedUnits() []string {
	prio := map[string]int{"ns/op": 0, "MB/s": 1, "B/op": 2, "allocs/op": 3}
	us := append([]string(nil), a.units...)
	sort.SliceStable(us, func(i, j int) bool {
		pi, oki := prio[us[i]]
		pj, okj := prio[us[j]]
		switch {
		case oki && okj:
			return pi < pj
		case oki:
			return true
		case okj:
			return false
		default:
			return false // stable: keep first-seen order for unknown units
		}
	})
	return us
}

func (a *aggregator) write(w io.Writer) {
	units := a.orderedUnits()

	// Provenance header: the run count (so a reader can confirm median-of-runs)
	// and the toolchain/arch that produced the numbers, captured in-container.
	fmt.Fprintf(w, "# runs=%d\n", stat.Runs())
	fmt.Fprintln(w, "# statistic=median-of-runs")
	fmt.Fprintf(w, "# go=%s\n", runtime.Version())
	fmt.Fprintf(w, "# goarch=%s\n", runtime.GOARCH)

	hdr := []string{"name", "iters"}
	for _, u := range units {
		hdr = append(hdr, u, u+"_min", u+"_max")
	}
	fmt.Fprintln(w, "# "+strings.Join(hdr, "\t"))

	for _, b := range a.benches {
		row := []string{b.name, strconv.FormatInt(stat.Collect(b.iters).Median, 10)}
		for _, u := range units {
			vs := b.metrics[u]
			if len(vs) == 0 {
				row = append(row, "-", "-", "-")
				continue
			}
			s := stat.Collect(vs)
			row = append(row, fmtNum(s.Median), fmtNum(s.Min), fmtNum(s.Max))
		}
		fmt.Fprintln(w, strings.Join(row, "\t"))
	}
}

// fmtNum formats a metric value without an exponent (so ns/op reads "12563594",
// not "1.2563594e+07") and with the shortest exact representation.
func fmtNum(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
