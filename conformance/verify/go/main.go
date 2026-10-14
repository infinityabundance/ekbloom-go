// Command conformance verifies the Go implementation of EKBLOOM against the
// immutable, implementation-neutral golden set in conformance/manifest.json.
//
// It reads the manifest and every fixture under -root (the conformance/
// directory), checks each file's sha256, and for every fixture checks the Go
// implementation's decoded bytes, machine id, logical length, physical
// accounting, worst-case-step bound and (for negative fixtures) the SPEC error
// kind. It uses only the public ekbloom API and exits non-zero on any mismatch.
//
// Usage:
//
//	cd conformance/verify/go && go run . -root ../..
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

func main() {
	root := flag.String("root", "../..", "path to the conformance/ directory")
	quiet := flag.Bool("quiet", false, "only print the final result line")
	jsonPath := flag.String("json", "", "write per-fixture observations as JSON to this path")
	flag.Parse()

	rep, err := Verify(*root, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "conformance: %v\n", err)
		os.Exit(1)
	}

	if *jsonPath != "" {
		if err := writeJSON(*jsonPath, rep); err != nil {
			fmt.Fprintf(os.Stderr, "conformance: write %s: %v\n", *jsonPath, err)
			os.Exit(1)
		}
	}

	for _, f := range rep.Failures {
		fmt.Fprintf(os.Stderr, "FAIL %s\n", f)
	}
	status := "pass"
	if !rep.OK() {
		status = "fail"
	}
	if !*quiet {
		fmt.Printf("conformance(go): %s — fixtures=%d checks=%d failures=%d (ekbloom-go public API)\n",
			status, rep.Fixtures, rep.Checks, len(rep.Failures))
	}
	fmt.Printf("RESULT go=%s fixtures=%d checks=%d failures=%d\n",
		status, rep.Fixtures, rep.Checks, len(rep.Failures))
	if !rep.OK() {
		os.Exit(1)
	}
}

// jsonReport is the on-disk shape of the per-fixture observations. It is
// deliberately small and stable: an independent comparator (which imports
// neither implementation) reads it to detect cross-implementation differences.
type jsonReport struct {
	Implementation string          `json:"implementation"`
	Fixtures       []FixtureResult `json:"fixtures"`
	Failures       []string        `json:"failures"`
}

func writeJSON(path string, rep Report) error {
	doc := jsonReport{Implementation: "go", Fixtures: rep.Results, Failures: rep.Failures}
	if doc.Fixtures == nil {
		doc.Fixtures = []FixtureResult{}
	}
	if doc.Failures == nil {
		doc.Failures = []string{}
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
