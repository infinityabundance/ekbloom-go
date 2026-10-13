// Command testsum reads `go test -json` events on stdin and writes a small,
// deterministic JSON summary (counts of pass/fail/skip actions) to stdout.
//
// Usage: go test -json ./... | go run ./internal/tools/testsum > summary.json
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type event struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
}

func main() {
	pass := map[string]bool{}
	fail := map[string]bool{}
	skip := map[string]bool{}
	pkgs := map[string]bool{}

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var e event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			continue
		}
		if e.Package == "" {
			continue
		}
		pkgs[e.Package] = true
		key := e.Package
		if e.Test != "" {
			key = e.Package + "/" + e.Test
		}
		switch e.Action {
		case "pass":
			pass[key] = true
		case "fail":
			fail[key] = true
		case "skip":
			skip[key] = true
		}
	}
	out := struct {
		Packages int  `json:"packages"`
		Passed   int  `json:"passed"`
		Failed   int  `json:"failed"`
		Skipped  int  `json:"skipped"`
		OK       bool `json:"ok"`
	}{len(pkgs), len(pass), len(fail), len(skip), len(fail) == 0}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
}
