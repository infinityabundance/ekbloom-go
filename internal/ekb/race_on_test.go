//go:build race

package ekb

// raceDetectorEnabled is true in a -race build. Race instrumentation adds memory
// accesses to every read and inflates every measured latency (~5-25x here), so the
// timing harnesses do not write their evidence under -race: `go test -race ./...` is
// the correctness gate, and timing evidence is generated without it (the valid
// measurement method). See writeTimingEvidence in helper_test.go.
const raceDetectorEnabled = true
