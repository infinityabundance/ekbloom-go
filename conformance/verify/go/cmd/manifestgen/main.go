// Command manifestgen is the AUTHORING tool for the conformance golden set.
//
// It is NOT part of the conformance verifiers and is never run at test time.
// It performs a one-time, deterministic materialisation of conformance/manifest.json:
//
//   - copies the Rust-written bodies from testdata/fixtures/ into
//     conformance/fixtures/rust/ (immutable golden copies, sha256 recorded);
//   - synthesises the negative fixtures (truncations, bit flips per section
//     kind, bad magic, future version, unknown machine/version) into
//     conformance/fixtures/negative/ from those bases;
//   - consumes the Go-written bodies already staged in conformance/fixtures/go/;
//   - writes expectations, marking each field "derived" (from SPEC.md alone) or
//     "asserted" (recorded from a reference run).
//
// The golden files and manifest are the deliverable; running this again without a
// format-version bump would defeat the immutability rule in conformance/SPEC.md.
//
// Usage (from conformance/verify/go):
//
//	go run ./cmd/manifestgen -repo ../../.. -conf ../..
package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"ekbloomconformance/blake3"

	ekbloom "github.com/infinityabundance/ekbloom-go"
)

const formatVersion = "ekb-format-0.3-conformance-1"

// GenesisHeader field offsets (SPEC.md §3.1).
const (
	offMagic       = 0
	offMajor       = 8
	offMinor       = 10
	offHeaderLen   = 12
	offFlags       = 16
	offObjectID    = 24
	offLogical     = 40
	offLogicalByte = 48
	offCoreLen     = 56
	offMachine     = 64
	offCoreVersion = 68
	headerBytes    = 80
)

const (
	kindHistoryFull      = 2
	kindCheckpoints      = 3
	kindBloomGlobal      = 4
	kindBloomContext     = 5
	kindBloomRegion      = 6
	kindBloomPath        = 7
	kindCertificate      = 8
	kindManifest         = 9
	kindAuth             = 10
	kindHistorySegmented = 11
	kindTiles            = 12
)

func align8(n uint64) uint64 { return (n + 7) &^ 7 }

// ---- expectations helper types (mirror the verifier's manifest schema) ----

type expected struct {
	MachineID       *uint32  `json:"machine_id"`
	LogicalLen      *uint64  `json:"logical_len"`
	PhysicalBytes   *uint64  `json:"physical_bytes"`
	Blake3OfDecoded *string  `json:"blake3_of_decoded"`
	ErrorKind       *string  `json:"error_kind"`
	ErrorKindAccept []string `json:"error_kind_accept"`
	WorstCaseSteps  *uint64  `json:"worst_case_steps"`
}

type sourceMap struct {
	MachineID       string `json:"machine_id"`
	LogicalLen      string `json:"logical_len"`
	PhysicalBytes   string `json:"physical_bytes"`
	Blake3OfDecoded string `json:"blake3_of_decoded"`
	ErrorKind       string `json:"error_kind"`
	WorstCaseSteps  string `json:"worst_case_steps"`
}

type fixture struct {
	Name       string    `json:"name"`
	File       string    `json:"file"`
	SHA256     string    `json:"sha256"`
	Origin     string    `json:"origin"`
	Polarity   string    `json:"polarity"`
	SrcFile    *string   `json:"src_file"`
	DictFile   *string   `json:"dict_file"`
	AuthKeyHex *string   `json:"auth_key_hex"`
	Mutation   *string   `json:"mutation"`
	Expected   expected  `json:"expected"`
	Source     sourceMap `json:"expectation_source"`
}

type divergence struct {
	Fixture string `json:"fixture"`
	Go      string `json:"go"`
	Rust    string `json:"rust"`
	Note    string `json:"note"`
	Rule    string `json:"rule"`
}

type manifest struct {
	FormatVersion    string       `json:"format_version"`
	Authority        string       `json:"authority"`
	Immutable        bool         `json:"immutable"`
	Note             string       `json:"note"`
	DivergencePolicy string       `json:"divergence_policy"`
	Fixtures         []fixture    `json:"fixtures"`
	Divergences      []divergence `json:"divergences"`
}

// ---- rust fixtures.json ----

type rustFixtureMeta struct {
	Name          string `json:"name"`
	MachineID     uint32 `json:"machine_id"`
	LogicalLen    uint64 `json:"logical_len"`
	PhysicalBytes uint64 `json:"physical_bytes"`
	Dict          string `json:"dict"`
}

type fixturesFile struct {
	Fixtures []rustFixtureMeta `json:"fixtures"`
}

// ---- SPEC-level layout parser (independent of the ekbloom implementation) ----

type section struct {
	kind   uint32
	shOff  uint64
	payOff uint64
	payLen uint64
}

type layout struct {
	coreStart uint64
	coreLen   uint64
	coreEnd   uint64
	sections  []section
	footerOff uint64
}

func parseLayout(b []byte) (layout, error) {
	var l layout
	if len(b) < headerBytes {
		return l, fmt.Errorf("short")
	}
	l.coreLen = binary.LittleEndian.Uint64(b[offCoreLen:])
	l.coreStart = headerBytes
	l.coreEnd = headerBytes + l.coreLen
	if uint64(len(b)) < l.coreEnd {
		return l, fmt.Errorf("core truncated")
	}
	pos := align8(l.coreEnd)
	total := uint64(len(b))
	for pos < total {
		if pos+40 > total {
			return l, fmt.Errorf("section header truncated")
		}
		kind := binary.LittleEndian.Uint32(b[pos:])
		length := binary.LittleEndian.Uint64(b[pos+16:])
		payOff := pos + 40
		payLen := length
		if payOff+payLen > total {
			return l, fmt.Errorf("section payload truncated")
		}
		l.sections = append(l.sections, section{kind: kind, shOff: pos, payOff: payOff, payLen: payLen})
		pos = align8(payOff + payLen)
		if kind == kindManifest {
			// Optional AUTH immediately follows the manifest.
			if pos+40 <= total && binary.LittleEndian.Uint32(b[pos:]) == kindAuth {
				l.sections = append(l.sections, section{kind: kindAuth, shOff: pos, payOff: pos + 40, payLen: binary.LittleEndian.Uint64(b[pos+16:])})
				pos = align8(pos + 40 + binary.LittleEndian.Uint64(b[pos+16:]))
			}
			l.footerOff = pos
			return l, nil
		}
	}
	return l, fmt.Errorf("no manifest section")
}

func (l layout) find(kind uint32) (section, bool) {
	for _, s := range l.sections {
		if s.kind == kind {
			return s, true
		}
	}
	return section{}, false
}

// rewriteManifest patches the manifest payload, then repairs the section CRC, the
// footer manifest_hash and the footer checksum so the file stays structurally
// self-consistent and the corruption is a *semantic* (not checksum) failure.
func rewriteManifest(b []byte, l layout, patch func(p []byte)) error {
	man, ok := l.find(kindManifest)
	if !ok {
		return fmt.Errorf("no manifest")
	}
	payload := b[man.payOff : man.payOff+man.payLen]
	patch(payload)
	// Section CRC-32/IEEE lives at shOff+32.
	binary.LittleEndian.PutUint32(b[man.shOff+32:], crc32.ChecksumIEEE(payload))
	// Footer: manifest_hash[16] at footerOff+32, footer_checksum u32 at footerOff+56
	// over bytes [0,56) ++ [60,64).
	h := blake3.Sum(payload)
	copy(b[l.footerOff+32:l.footerOff+48], h[:16])
	binary.LittleEndian.PutUint32(b[l.footerOff+56:], footerChecksum(b[l.footerOff:l.footerOff+64]))
	return nil
}

func footerChecksum(footer []byte) uint32 {
	var buf [60]byte
	copy(buf[0:56], footer[0:56])
	copy(buf[56:], footer[60:64])
	return crc32.ChecksumIEEE(buf[:])
}

// ---- main ----

func main() {
	repo := flag.String("repo", "../../..", "repository root")
	conf := flag.String("conf", "../..", "conformance/ directory")
	flag.Parse()

	must(os.MkdirAll(filepath.Join(*conf, "fixtures", "rust"), 0o755))
	must(os.MkdirAll(filepath.Join(*conf, "fixtures", "go"), 0o755))
	must(os.MkdirAll(filepath.Join(*conf, "fixtures", "negative"), 0o755))

	// ---- 1. Copy Rust-written golden bodies -----------------------------
	rustMeta := loadRustMeta(filepath.Join(*repo, "testdata", "fixtures.json"))
	rustNames := make([]string, 0, len(rustMeta))
	for n := range rustMeta {
		rustNames = append(rustNames, n)
	}
	sort.Strings(rustNames)

	var fixtures []fixture
	for _, name := range rustNames {
		meta := rustMeta[name]
		ekbDst := filepath.Join(*conf, "fixtures", "rust", name+".ekb")
		srcDst := filepath.Join(*conf, "fixtures", "rust", name+".src")
		copyFile(filepath.Join(*repo, "testdata", "fixtures", name+".ekb"), ekbDst)
		copyFile(filepath.Join(*repo, "testdata", "fixtures", name+".src"), srcDst)
		var dictRel *string
		if meta.Dict != "" {
			dictDst := filepath.Join(*conf, "fixtures", "rust", name+".dict")
			copyFile(filepath.Join(*repo, "testdata", meta.Dict), dictDst)
			rel := "fixtures/rust/" + name + ".dict"
			dictRel = &rel
		}
		f := positiveFixture("rust_"+name, ekbDst, srcDst, dictRel, nil, *conf)
		// machine_id is derived from the header; cross-check against fixtures.json.
		if f.Expected.MachineID == nil || *f.Expected.MachineID != meta.MachineID {
			panic(fmt.Sprintf("%s: machine_id mismatch with fixtures.json", name))
		}
		f.Expected.PhysicalBytes = u64(meta.PhysicalBytes)
		f.Source.PhysicalBytes = "asserted" // recorded by the Rust `inspect` run during generation
		if f.Expected.LogicalLen == nil || *f.Expected.LogicalLen != meta.LogicalLen {
			panic(fmt.Sprintf("%s: logical_len mismatch with fixtures.json", name))
		}
		fixtures = append(fixtures, f)
	}

	// ---- 2. Go-written bodies (staged separately by roundtripgen) --------
	goFixtures := collectGoFixtures(*conf)
	fixtures = append(fixtures, goFixtures...)

	// ---- 3. Negative fixtures -------------------------------------------
	fixtures = append(fixtures, buildNegatives(*conf)...)

	sort.Slice(fixtures, func(i, j int) bool { return fixtures[i].Name < fixtures[j].Name })

	m := manifest{
		FormatVersion: formatVersion,
		Authority:     "conformance/SPEC.md",
		Immutable:     true,
		Note: "Golden set for the EKBLOOM .ekb byte format. Immutable: regenerating any file requires a format-version bump and a note in conformance/SPEC.md. " +
			"format_version keys the fixture corpus, not the wire format (which is 0.3). See conformance/README.md.",
		DivergencePolicy: divergencePolicy,
		Fixtures:         fixtures,
		Divergences:      divergences(),
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	must(err)
	raw = append(raw, '\n')
	must(os.WriteFile(filepath.Join(*conf, "manifest.json"), raw, 0o644))

	pos, neg := 0, 0
	for _, f := range fixtures {
		if f.Polarity == "positive" {
			pos++
		} else {
			neg++
		}
	}
	fmt.Printf("manifestgen: wrote %d fixtures (%d positive, %d negative) to %s\n",
		len(fixtures), pos, neg, filepath.Join(*conf, "manifest.json"))
}

// positiveFixture builds a positive fixture entry and derives the expectations
// that SPEC.md fixes independently of any decoder.
func positiveFixture(name, ekbPath, srcPath string, dictRel, authKey *string, conf string) fixture {
	data := mustRead(ekbPath)
	src := mustRead(srcPath)
	n := uint64(len(src))
	digest := blake3.Sum(src)
	dec := hex.EncodeToString(digest[:])
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	mid := binary.LittleEndian.Uint32(data[offMachine:])
	srcRel := relTo(conf, srcPath)
	return fixture{
		Name:       name,
		File:       relTo(conf, ekbPath),
		SHA256:     sha,
		Origin:     originOf(name),
		Polarity:   "positive",
		SrcFile:    &srcRel,
		DictFile:   dictRel,
		AuthKeyHex: authKey,
		Expected: expected{
			MachineID:       u32(mid),
			LogicalLen:      u64(n),
			Blake3OfDecoded: &dec,
			WorstCaseSteps:  u64(n), // WorstCaseStepsFor(0, N) == N per SPEC §6
			ErrorKindAccept: []string{},
		},
		Source: sourceMap{
			MachineID:       "derived",
			LogicalLen:      "derived",
			Blake3OfDecoded: "derived",
			WorstCaseSteps:  "derived",
		},
	}
}

func originOf(name string) string {
	if strings.HasPrefix(name, "go_") {
		return "go"
	}
	return "rust"
}

// collectGoFixtures consumes the Go-written bodies staged in fixtures/go/.
func collectGoFixtures(conf string) []fixture {
	dir := filepath.Join(conf, "fixtures", "go")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".ekb" {
			names = append(names, e.Name()[:len(e.Name())-4])
		}
	}
	sort.Strings(names)
	var out []fixture
	for _, n := range names {
		ekbPath := filepath.Join(dir, n+".ekb")
		srcPath := filepath.Join(dir, n+".src")
		if _, err := os.Stat(srcPath); err != nil {
			continue
		}
		var dictRel *string
		if _, err := os.Stat(filepath.Join(dir, n+".dict")); err == nil {
			rel := "fixtures/go/" + n + ".dict"
			dictRel = &rel
		}
		var authKey *string
		if n == "go_auth" {
			k := keyHex()
			authKey = &k
		}
		f := positiveFixture(n, ekbPath, srcPath, dictRel, authKey, conf)
		// physical_bytes is not independently derivable in-SPEC without
		// re-implementing the accounting walk; record it from a Go run and let
		// the Rust verifier cross-check it.
		phys := goPhysicalBytes(conf, ekbPath, dictRel, authKey)
		f.Expected.PhysicalBytes = &phys
		f.Source.PhysicalBytes = "asserted"
		out = append(out, f)
	}
	return out
}

func goPhysicalBytes(conf, ekbPath string, dictRel, authKey *string) uint64 {
	data := mustRead(ekbPath)
	r := mustOpen(data, conf, dictRel, authKey)
	return r.PhysicalBytes()
}

func mustOpen(data []byte, conf string, dictRel, authKey *string) *ekbloom.Reader {
	var dict *ekbloom.Dictionary
	if dictRel != nil {
		d, err := ekbloom.DictionaryFromBytes(mustRead(filepath.Join(conf, *dictRel)))
		must(err)
		dict = &d
	}
	if authKey != nil {
		var key [32]byte
		b, err := hex.DecodeString(*authKey)
		must(err)
		copy(key[:], b)
		r, err := ekbloom.OpenAuthenticated(data, key)
		must(err)
		return r
	}
	if dict != nil {
		r, err := ekbloom.OpenWithDict(data, *dict)
		must(err)
		return r
	}
	r, err := ekbloom.Open(data)
	must(err)
	return r
}

// goObservedKind returns the SPEC kind name the Go implementation surfaces for a
// malformed body.
func goObservedKind(data []byte) string {
	_, err := ekbloom.Open(data)
	if err == nil {
		return ""
	}
	return goKindName(err)
}

func goKindName(err error) string {
	e, ok := err.(*ekbloom.Error)
	if !ok {
		return "ErrNonConforming"
	}
	switch e.Kind {
	case ekbloom.KindBadMagic:
		return "ErrBadMagic"
	case ekbloom.KindUnsupportedVersion:
		return "ErrUnsupportedVersion"
	case ekbloom.KindUnsupportedCoreVersion:
		return "ErrUnsupportedCoreVersion"
	case ekbloom.KindUnsupportedManifestVersion:
		return "ErrUnsupportedManifestVersion"
	case ekbloom.KindTruncated:
		return "ErrTruncated"
	case ekbloom.KindChecksumMismatch:
		return "ErrChecksumMismatch"
	case ekbloom.KindCorrupt:
		return "ErrCorrupt"
	case ekbloom.KindBadSection:
		return "ErrBadSection"
	case ekbloom.KindOverflow:
		return "ErrOverflow"
	case ekbloom.KindUnknownMachine:
		return "ErrUnknownMachine"
	case ekbloom.KindCoreTooShort:
		return "ErrCoreTooShort"
	case ekbloom.KindOutOfBounds:
		return "ErrOutOfBounds"
	case ekbloom.KindRecoveryFailed:
		return "ErrRecoveryFailed"
	}
	return fmt.Sprintf("ErrKind(%d)", int(e.Kind))
}

// negSpec describes one negative fixture: a base body plus a mutation, the
// canonical SPEC error kind, and (for commit-block corruptions where the two
// implementations surface different typed errors) the wider accept set.
type negSpec struct {
	name    string
	base    string // base fixture stem under fixtures/rust
	mutate  func(b []byte, l layout) []byte
	derived string // non-empty => the kind is fixed by SPEC alone
	accept  []string
	note    string
}

func baseBytes(conf, base string) []byte {
	for _, sub := range []string{"rust", "go"} {
		p := filepath.Join(conf, "fixtures", sub, base+".ekb")
		if _, err := os.Stat(p); err == nil {
			return mustRead(p)
		}
	}
	panic("negative base not found: " + base)
}

func buildNegatives(conf string) []fixture {
	root := "m3_root_rand"            // machine 3, core-only
	full := "m1_full_rand"            // machine 1, full: history
	sparse := "m1_sparse64_text"      // machine 1, checkpoints
	path4 := "m1_path4_rand"          // machine 1, path: bloom-path + certificate
	seg := "m6_root_mixed"            // machine 6, segmented core
	dict := "m9_full_small"           // machine 9, history
	goUniv := "go_m1_univ4_text"      // machine 1, bloom-global + certificate
	goRegion := "go_m1_regional_text" // machine 1, bloom-region + certificate
	goTiles := "go_tiles"             // machine 1, tiles
	goAuth := "go_auth"               // machine 1, history + auth

	set := func(off int, val []byte) func([]byte, layout) []byte {
		return func(b []byte, _ layout) []byte { copy(b[off:], val); return b }
	}
	u16v := func(v uint16) []byte { o := make([]byte, 2); binary.LittleEndian.PutUint16(o, v); return o }
	u32v := func(v uint32) []byte { o := make([]byte, 4); binary.LittleEndian.PutUint32(o, v); return o }
	u64v := func(v uint64) []byte { o := make([]byte, 8); binary.LittleEndian.PutUint64(o, v); return o }
	flipCore := func(rel int) func([]byte, layout) []byte {
		return func(b []byte, l layout) []byte { b[l.coreStart+uint64(rel)] ^= 0x01; return b }
	}
	inPayload := func(kind uint32, rel int) func([]byte, layout) []byte {
		return func(b []byte, l layout) []byte {
			s, ok := l.find(kind)
			if !ok {
				panic("missing section kind")
			}
			b[s.payOff+uint64(rel)] ^= 0x40
			return b
		}
	}

	// Accept sets (SPEC §7): a torn first commit may surface either the raw
	// structural error or the wrapped recovery failure.
	commitCorruptAccept := []string{"ErrChecksumMismatch", "ErrRecoveryFailed", "ErrCorrupt"}
	truncCommitAccept := []string{"ErrTruncated", "ErrRecoveryFailed"}
	badSectionAccept := []string{"ErrBadSection", "ErrRecoveryFailed", "ErrCorrupt"}
	mismatchAccept := []string{"ErrBadSection", "ErrCorrupt", "ErrRecoveryFailed"}
	authAccept := []string{"ErrBadSection", "ErrChecksumMismatch", "ErrRecoveryFailed", "ErrCorrupt"}
	coreChecksumAccept := []string{"ErrCorrupt"}
	footerChecksumAccept := []string{"ErrChecksumMismatch", "ErrBadSection", "ErrRecoveryFailed", "ErrCorrupt"}

	specs := []negSpec{
		// ---- malformed at or before the core (implementation-independent) ----
		{"neg_empty", root, func(b []byte, _ layout) []byte { return b[:0] }, "ErrTruncated", nil, "empty file"},
		{"neg_header_short", root, func(b []byte, _ layout) []byte { return b[:40] }, "ErrTruncated", nil, "truncated inside the 80-byte genesis header"},
		{"neg_bad_magic", root, func(b []byte, _ layout) []byte { b[0] ^= 0xff; return b }, "ErrBadMagic", nil, "core byte 0 of the magic flipped"},
		{"neg_bad_magic_all", root, set(offMagic, []byte("NOTEKBXX")), "ErrBadMagic", nil, "magic replaced"},
		{"neg_header_len", root, set(offHeaderLen, u32v(81)), "ErrBadSection", nil, "header_len field set to 81"},
		{"neg_feature_flags", root, set(offFlags, u64v(1)), "ErrBadSection", nil, "feature_flags set non-zero"},
		{"neg_format_major", root, set(offMajor, u16v(1)), "ErrUnsupportedVersion", nil, "format_major set to 1"},
		{"neg_format_minor_future", root, set(offMinor, u16v(4)), "ErrUnsupportedVersion", nil, "format_minor set beyond the supported 3"},
		{"neg_core_version", root, set(offCoreVersion, u16v(9)), "ErrUnsupportedCoreVersion", nil, "core_version 9 on a known machine"},
		{"neg_trunc_core", root, func(b []byte, l layout) []byte { return b[:l.coreStart+l.coreLen/2] }, "ErrTruncated", nil, "truncated midway through the core"},
		{"neg_core_bitflip", root, flipCore(10), "ErrChecksumMismatch", coreChecksumAccept, "one core byte flipped: manifest core_hash no longer matches"},
		{"neg_core_bitflip_m6", seg, flipCore(7), "ErrChecksumMismatch", coreChecksumAccept, "core byte flipped in a machine-6 (SEG) core"},
		{"neg_core_bitflip_m9", dict, flipCore(30), "ErrChecksumMismatch", coreChecksumAccept, "core byte flipped in a machine-9 (DICT) core"},
		{"neg_core_only", root, func(b []byte, l layout) []byte { return b[:align8(l.coreEnd)] }, "ErrRecoveryFailed", nil, "valid header+core, no commit block"},

		// ---- header/manifest cross-field disagreement (typed error differs) ----
		{"neg_unknown_machine", root, set(offMachine, u32v(42)), "", mismatchAccept, "header machine id 42 (unregistered)"},
		{"neg_machine_mismatch", root, set(offMachine, u32v(1)), "", mismatchAccept, "header machine id disagrees with the manifest"},
		{"neg_object_id_mismatch", root, func(b []byte, _ layout) []byte { b[offObjectID] ^= 0x01; return b }, "", mismatchAccept, "header object_id disagrees with the manifest"},
		{"neg_logical_mismatch", root, set(offLogical, u64v(4097)), "", mismatchAccept, "header logical_symbols disagrees with the manifest"},

		// ---- commit-block corruption (torn-tail: raw vs wrapped typed error) ----
		{"neg_trunc_commit", root, func(b []byte, _ layout) []byte { return b[:len(b)-80] }, "", truncCommitAccept, "truncated inside the single commit block"},
		{"neg_manifest_bitflip", root, inPayload(kindManifest, 4), "", commitCorruptAccept, "manifest payload byte flipped (section CRC-32 now wrong)"},
		{"neg_history_bitflip", full, inPayload(kindHistoryFull, 40), "", commitCorruptAccept, "history-full payload byte flipped"},
		{"neg_checkpoint_bitflip", sparse, inPayload(kindCheckpoints, 16), "", commitCorruptAccept, "checkpoint payload byte flipped"},
		{"neg_bloom_path_bitflip", path4, inPayload(kindBloomPath, 40), "", commitCorruptAccept, "bloom-path payload byte flipped"},
		{"neg_bloom_global_bitflip", goUniv, inPayload(kindBloomGlobal, 100), "", commitCorruptAccept, "bloom-global payload byte flipped"},
		{"neg_bloom_region_bitflip", goRegion, inPayload(kindBloomRegion, 100), "", commitCorruptAccept, "bloom-region payload byte flipped"},
		{"neg_certificate_bitflip", path4, inPayload(kindCertificate, 40), "", commitCorruptAccept, "certificate payload byte flipped"},
		{"neg_tiles_bitflip", goTiles, inPayload(kindTiles, 40), "", commitCorruptAccept, "tiles payload byte flipped"},
		{"neg_auth_bitflip", goAuth, inPayload(kindAuth, 4), "", authAccept, "AUTH payload byte flipped"},
		{"neg_footer_marker", root, func(b []byte, l layout) []byte { b[l.footerOff+48] ^= 0xff; return b }, "", badSectionAccept, "commit marker corrupted"},
		{"neg_footer_checksum", root, func(b []byte, l layout) []byte { b[l.footerOff] ^= 0x01; return b }, "", footerChecksumAccept, "footer generation flipped without fixing the footer checksum"},
		{"neg_section_kind", root, func(b []byte, l layout) []byte {
			man, _ := l.find(kindManifest)
			binary.LittleEndian.PutUint32(b[man.shOff:], 99)
			return b
		}, "", badSectionAccept, "manifest section kind replaced with an unknown wire code"},
		{"neg_manifest_version", root, func(b []byte, l layout) []byte {
			must(rewriteManifest(b, l, func(p []byte) { binary.LittleEndian.PutUint32(p[0:], 2) }))
			return b
		}, "", []string{"ErrUnsupportedManifestVersion", "ErrRecoveryFailed"}, "manifest record version 2 (CRC + footer hash repaired)"},
	}

	out := make([]fixture, 0, len(specs))
	for _, s := range specs {
		b := baseBytes(conf, s.base)
		l, err := parseLayout(b)
		must(err)
		mutated := s.mutate(append([]byte(nil), b...), l)
		name := s.name
		path := filepath.Join(conf, "fixtures", "negative", name+".ekb")
		must(os.WriteFile(path, mutated, 0o644))
		sum := sha256.Sum256(mutated)
		sha := hex.EncodeToString(sum[:])

		kind := s.derived
		srcNote := "derived"
		if kind == "" {
			kind = goObservedKind(mutated)
			if kind == "" {
				panic(fmt.Sprintf("%s: negative fixture unexpectedly opened", name))
			}
			srcNote = "asserted"
		} else {
			if got := goObservedKind(mutated); got != kind {
				panic(fmt.Sprintf("%s: Go observed %s, SPEC-derived %s", name, got, kind))
			}
		}

		rel := relTo(conf, path)
		note := s.note
		accept := s.accept
		if accept == nil {
			accept = []string{}
		}
		out = append(out, fixture{
			Name:     name,
			File:     rel,
			SHA256:   sha,
			Origin:   "negative",
			Polarity: "negative",
			Mutation: &note,
			Expected: expected{
				ErrorKind:       &kind,
				ErrorKindAccept: accept,
			},
			Source: sourceMap{ErrorKind: srcNote},
		})
	}
	return out
}

// divergencePolicy is the manifest-level statement of the rule the independent
// comparator enforces (see conformance/tools/divergence and SPEC.md §10).
const divergencePolicy = "The observed cross-implementation divergence set MUST be a subset of the ruled set in `divergences`; " +
	"each rule MUST cite a SPEC section that exists in SPEC.md; an accept set with more than one kind is a ruled divergence " +
	"and MUST be listed; a new accepted difference requires a SPEC section 10 change-control entry. " +
	"Enforced by conformance/tools/divergence (which imports neither implementation)."

// SPEC citations for the ruled divergence families.
const (
	ruleCrossField = "SPEC §7.4 (cross-field disagreement)"
	ruleTorn       = "SPEC §7.2 (torn-first-commit)"
	ruleLocalize   = "SPEC §7.3 (structural rejection vs localization)"
)

// divergences documents the cross-implementation differences the suite accepts
// (each fixture's accept set already permits both kinds), each citing the SPEC
// section that permits it. Filled in from runs of both verifiers during
// authoring; enforced by conformance/tools/divergence.
func divergences() []divergence {
	same := map[string]string{
		"neg_machine_mismatch":   "header/manifest machine id disagree",
		"neg_unknown_machine":    "unknown header machine id reaches the manifest cross-check first",
		"neg_object_id_mismatch": "header/manifest object_id disagree",
		"neg_logical_mismatch":   "header/manifest logical length disagree",
	}
	out := []divergence{
		{"neg_machine_mismatch", "ErrBadSection", "ErrCorrupt", same["neg_machine_mismatch"], ruleCrossField},
		{"neg_unknown_machine", "ErrBadSection", "ErrCorrupt", same["neg_unknown_machine"], ruleCrossField},
		{"neg_object_id_mismatch", "ErrBadSection", "ErrCorrupt", same["neg_object_id_mismatch"], ruleCrossField},
		{"neg_logical_mismatch", "ErrBadSection", "ErrCorrupt", same["neg_logical_mismatch"], ruleCrossField},
		{"neg_trunc_commit", "ErrRecoveryFailed", "ErrTruncated", "torn first commit: Go wraps the torn error as RecoveryFailed, Rust surfaces the raw Truncated", ruleTorn},
	}
	torn := []string{"neg_manifest_bitflip", "neg_history_bitflip", "neg_checkpoint_bitflip",
		"neg_bloom_path_bitflip", "neg_bloom_global_bitflip", "neg_bloom_region_bitflip",
		"neg_certificate_bitflip", "neg_tiles_bitflip", "neg_footer_checksum"}
	for _, n := range torn {
		out = append(out, divergence{n, "ErrRecoveryFailed", "ErrChecksumMismatch", "torn first commit: Go wraps the section checksum mismatch, Rust surfaces it raw", ruleTorn})
	}
	for _, n := range []string{"neg_core_bitflip", "neg_core_bitflip_m6", "neg_core_bitflip_m9"} {
		out = append(out, divergence{n, "ErrChecksumMismatch", "ErrCorrupt", "core hash mismatch: Go surfaces ChecksumMismatch(section=core), Rust surfaces CorruptGeneration", ruleLocalize})
	}
	out = append(out,
		divergence{"neg_footer_marker", "ErrRecoveryFailed", "ErrBadSection", "torn first commit: Go wraps the footer marker error, Rust surfaces it raw", ruleTorn},
		divergence{"neg_section_kind", "ErrRecoveryFailed", "ErrBadSection", "torn first commit: Go wraps the unknown-section-kind error, Rust surfaces it raw", ruleTorn},
		divergence{"neg_auth_bitflip", "ErrRecoveryFailed", "ErrBadSection", "torn first commit after the AUTH section fails its CRC", ruleTorn},
		divergence{"neg_manifest_version", "ErrRecoveryFailed", "ErrUnsupportedManifestVersion", "torn first commit: Go wraps the manifest-version error, Rust surfaces it raw", ruleTorn},
	)
	return out
}

// ---- small helpers ----

func loadRustMeta(path string) map[string]rustFixtureMeta {
	raw := mustRead(path)
	var ff fixturesFile
	must(json.Unmarshal(raw, &ff))
	out := make(map[string]rustFixtureMeta, len(ff.Fixtures))
	for _, f := range ff.Fixtures {
		out[f.Name] = f
	}
	return out
}

func keyHex() string {
	var key [32]byte
	for i := range key {
		key[i] = byte(i)
	}
	return hex.EncodeToString(key[:])
}

func relTo(base, path string) string {
	r, err := filepath.Rel(base, path)
	must(err)
	return filepath.ToSlash(r)
}

func copyFile(src, dst string) {
	must(os.WriteFile(dst, mustRead(src), 0o644))
}

func mustRead(p string) []byte {
	b, err := os.ReadFile(p)
	must(err)
	return b
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func u32(v uint32) *uint32 { return &v }
func u64(v uint64) *uint64 { return &v }
