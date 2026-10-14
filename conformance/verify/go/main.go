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
	"flag"
	"fmt"
	"os"
)

func main() {
	root := flag.String("root", "../..", "path to the conformance/ directory")
	quiet := flag.Bool("quiet", false, "only print the final result line")
	flag.Parse()

	rep, err := Verify(*root, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "conformance: %v\n", err)
		os.Exit(1)
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
