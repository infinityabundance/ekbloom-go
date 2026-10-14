//go:build !race

package ekb

// raceDetectorEnabled is false in a normal (non -race) build, where the timing
// harnesses write their evidence. See writeTimingEvidence in helper_test.go.
const raceDetectorEnabled = false
