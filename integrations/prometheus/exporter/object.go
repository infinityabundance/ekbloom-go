package exporter

import (
	"fmt"
	"os"

	ekbloom "github.com/infinityabundance/ekbloom-go"
)

// object is a representative EKBLOOM object: the logical source bytes plus the
// machine-9 records and shared dictionary it was built from.
type object struct {
	source  []byte
	records [][]byte
	dict    ekbloom.Dictionary
}

// tokens is a skewed/repetitive text alphabet — the same shape the ycsb and
// pebble courts use — so the trained order-0 dictionary is genuinely
// representative of a structured value workload.
var tokens = []string{
	"id=", "user", "kind=", "alpha", "beta", "gamma", "region=", "us-east-1",
	"eu-west-1", "ap-south-1", "score=", "000", "111", "222", "status=",
	"active", "idle", "|",
}

// synthRecord builds one deterministic record of exactly n bytes.
func synthRecord(seed, n int) []byte {
	out := make([]byte, 0, n+16)
	k := uint64(seed)*2654435761 + 1
	for len(out) < n {
		k = k*6364136223846793005 + 1442695040888963407
		out = append(out, tokens[(k>>33)%uint64(len(tokens))]...)
	}
	return out[:n]
}

// buildObject loads a prebuilt fixture when configured, otherwise synthesizes a
// small corpus, trains the shared machine-9 dictionary with
// ekbloom.TrainDictionary, and concatenates the records into one representative
// object body (later sealed with ekbloom.WriteBodyWithDict).
func buildObject(cfg Config) (*object, error) {
	if cfg.FixturePath != "" || cfg.DictPath != "" {
		if cfg.FixturePath == "" || cfg.DictPath == "" {
			return nil, fmt.Errorf("both --fixture and --dict must be set to load a prebuilt object")
		}
		data, err := os.ReadFile(cfg.FixturePath)
		if err != nil {
			return nil, fmt.Errorf("read fixture: %w", err)
		}
		db, err := os.ReadFile(cfg.DictPath)
		if err != nil {
			return nil, fmt.Errorf("read dictionary: %w", err)
		}
		dict, err := ekbloom.DictionaryFromBytes(db)
		if err != nil {
			return nil, fmt.Errorf("DictionaryFromBytes: %w", err)
		}
		// The records are only used to exercise DecodeInto; any bytes the fixture
		// dictionary can encode are valid for that timing path.
		recs := make([][]byte, cfg.Records)
		for i := range recs {
			recs[i] = synthRecord(i, cfg.RecordBytes)
		}
		return &object{source: data, records: recs, dict: dict}, nil
	}

	recs := make([][]byte, cfg.Records)
	for i := range recs {
		recs[i] = synthRecord(i, cfg.RecordBytes)
	}
	dict, err := ekbloom.TrainDictionary(recs[:cfg.TrainRecords])
	if err != nil {
		return nil, fmt.Errorf("TrainDictionary: %w", err)
	}
	src := make([]byte, 0, cfg.Records*cfg.RecordBytes)
	for _, r := range recs {
		src = append(src, r...)
	}
	return &object{source: src, records: recs, dict: dict}, nil
}
