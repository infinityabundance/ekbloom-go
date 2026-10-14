// Command testsum reads `go test -json` events on stdin and writes a small,
// deterministic JSON summary (counts of pass/fail/skip actions) to stdout.
//
// It can also record provenance for external-reproduction artifacts, so the
// emitted JSON pins exactly which published image was verified:
//
//	go test -json ./... | go run ./internal/tools/testsum \
//	    -image ghcr.io/org/repo:test -tag test \
//	    -reference ghcr.io/org/repo@sha256:... -digest sha256:... -anonymous=true
//
// Usage: go test -json ./... | go run ./internal/tools/testsum > summary.json
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

type event struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
}

func main() {
	image := flag.String("image", "", "image reference that was tested (tag form)")
	tag := flag.String("tag", "", "image tag that was resolved")
	reference := flag.String("reference", "", "fully-qualified image reference@digest")
	digest := flag.String("digest", "", "immutable image digest that was verified")
	anonymous := flag.Bool("anonymous", false, "image was pulled without credentials")
	flag.Parse()

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
	// Provenance fields are omitted when unset, so callers that summarise a
	// local run (reproduce.sh, ci.sh, container-reproduce.sh) keep the original
	// five-field output byte-for-byte.
	out := struct {
		Image     string `json:"image,omitempty"`
		Tag       string `json:"tag,omitempty"`
		Reference string `json:"reference,omitempty"`
		Digest    string `json:"digest,omitempty"`
		Anonymous bool   `json:"pulled_anonymously,omitempty"`
		Packages  int    `json:"packages"`
		Passed    int    `json:"passed"`
		Failed    int    `json:"failed"`
		Skipped   int    `json:"skipped"`
		OK        bool   `json:"ok"`
	}{*image, *tag, *reference, *digest, *anonymous, len(pkgs), len(pass), len(fail), len(skip), len(fail) == 0}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
}
