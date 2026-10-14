package ekbloom

import "github.com/infinityabundance/ekbloom-go/internal/ekb"

// WriteBody serializes a body for (machineID, source) under profile into a valid
// `.ekb` image (generation 1).
func WriteBody(machineID uint32, source []byte, profile Profile) ([]byte, error) {
	return ekb.WriteBody(machineID, source, profile)
}

// WriteBodyAuthenticated is WriteBody with an opt-in keyed AUTH section.
func WriteBodyAuthenticated(machineID uint32, source []byte, profile Profile, key [32]byte) ([]byte, error) {
	return ekb.WriteBodyAuthenticated(machineID, source, profile, key)
}

// WriteBodyWithDict serializes a machine-9 body whose core references a shared
// trained dictionary.
func WriteBodyWithDict(source []byte, profile Profile, dict Dictionary) ([]byte, error) {
	return ekb.WriteBodyWithDict(source, profile, dict)
}

// WriteBodyTiles serializes a body with a tiles directory.
func WriteBodyTiles(machineID uint32, source []byte, tileShift uint8, presentTiles []uint64) ([]byte, error) {
	return ekb.WriteBodyTiles(machineID, source, tileShift, presentTiles)
}

// AppendGeneration appends an additive generation to an `.ekb` image.
func AppendGeneration(data []byte, machineID uint32, source []byte, profile Profile) ([]byte, uint64, error) {
	return ekb.AppendGeneration(data, machineID, source, profile)
}

// AppendGenerationAuthenticated is AppendGeneration with a keyed AUTH section.
func AppendGenerationAuthenticated(data []byte, machineID uint32, source []byte, profile Profile, key *[32]byte) ([]byte, uint64, error) {
	return ekb.AppendGenerationAuthenticated(data, machineID, source, profile, key)
}

// TruncateToValid truncates an image to its newest fully-valid generation.
func TruncateToValid(data []byte) ([]byte, uint64, error) { return ekb.TruncateToValid(data) }

// TrainDictionary trains a shared order-0 dictionary from a corpus (machine 9).
func TrainDictionary(records [][]byte) (Dictionary, error) { return ekb.TrainDictionary(records) }

// DictionaryFromBytes parses and validates a serialized dictionary.
func DictionaryFromBytes(b []byte) (Dictionary, error) { return ekb.DictionaryFromBytes(b) }

// RegisterDictionary installs d so a machine-9 core may reference it by id.
func RegisterDictionary(d Dictionary) { ekb.RegisterDictionary(d) }

// LookupDictionary resolves a registered dictionary by content id.
func LookupDictionary(id [16]byte) (Dictionary, bool) { return ekb.LookupDictionary(id) }
