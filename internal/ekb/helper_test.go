package ekb

import (
	"os"
	"path/filepath"
	"runtime"
)

// repoRoot is the repository root, computed from this file's location
// (<root>/internal/ekb/helper_test.go) so tests can find testdata/ and write
// evidence/ regardless of the working directory.
func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// readTestdata reads a file under testdata/ by relative path (for tests/fuzzers).
func readTestdata(rel string) ([]byte, error) {
	return os.ReadFile(filepath.Join(repoRoot(), "testdata", filepath.FromSlash(rel)))
}

// testdataPath joins parts under the repository's testdata/ directory.
func testdataPath(parts ...string) string {
	return filepath.Join(append([]string{repoRoot(), "testdata"}, parts...)...)
}

// writeEvidence writes an artifact under the repository's evidence/ directory.
// The variadic permission is accepted (and ignored) so call sites read naturally.
func writeEvidence(name string, b []byte, _ ...os.FileMode) error {
	return os.WriteFile(filepath.Join(repoRoot(), "evidence", name), b, 0o644)
}
