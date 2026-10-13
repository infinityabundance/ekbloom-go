package ekbloom

import (
	"os"
	"path/filepath"
)

// readTestdata reads a file under testdata/ by relative path (for tests/fuzzers).
func readTestdata(rel string) ([]byte, error) {
	return os.ReadFile(filepath.Join("testdata", filepath.FromSlash(rel)))
}
