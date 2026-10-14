// Command corpuscompare diffs two corpus E_V reports (Go vs Rust) produced by
// internal/tools/corpusbench and tools/fixturegen/src/bin/corpusbench.rs. It
// requires physical_bytes and logical_len to be identical for every (file,
// machine) pair; E_V = physical_bytes / logical_len then follows exactly. It
// prints a table of the comparison.
//
// Usage: corpuscompare <go.json> <rust.json>
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
)

type row struct {
	File     string  `json:"file"`
	Machine  uint32  `json:"machine_id"`
	Logical  uint64  `json:"logical_len"`
	Physical uint64  `json:"physical_bytes"`
	EV       float64 `json:"E_V"`
	P50      int64   `json:"byte_at_p50_ns"`
}

type report struct {
	Rows []row `json:"rows"`
}

func load(path string) report {
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var r report
	if err := json.Unmarshal(b, &r); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
		os.Exit(1)
	}
	return r
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: corpuscompare <go.json> <rust.json>")
		os.Exit(2)
	}
	g := load(os.Args[1])
	r := load(os.Args[2])

	type key struct {
		file    string
		machine uint32
	}
	rIndex := map[key]row{}
	for _, x := range r.Rows {
		rIndex[key{x.File, x.Machine}] = x
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "file\tm\tN\tphysical (go)\tphysical (rust)\tE_V\tmatch")

	fail := 0
	matched := 0
	for _, x := range g.Rows {
		k := key{x.File, x.Machine}
		y, ok := rIndex[k]
		if !ok {
			fmt.Fprintf(w, "%s\t%d\t%d\t%d\t-\t%.4f\tMISSING\n", x.File, x.Machine, x.Logical, x.Physical, x.EV)
			fail++
			continue
		}
		ok = x.Physical == y.Physical && x.Logical == y.Logical
		if !ok {
			fail++
		} else {
			matched++
		}
		status := "ok"
		if !ok {
			status = "MISMATCH"
		}
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%.4f\t%s\n",
			x.File, x.Machine, x.Logical, x.Physical, y.Physical, x.EV, status)
	}
	w.Flush()

	fmt.Printf("\n%d/%d rows identical (physical_bytes, logical_len, E_V)\n", matched, len(g.Rows))
	if fail > 0 {
		fmt.Printf("FAIL: %d row(s) differ\n", fail)
		os.Exit(1)
	}
	fmt.Println("PASS: Go and Rust agree on every corpus row")
}
