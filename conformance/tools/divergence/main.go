// Command divergence is an independent conformance comparator for the EKBLOOM
// golden set. It imports NEITHER implementation (nor the fixture generator): it
// reads manifest.json, the two verifiers' per-fixture JSON observations, and
// SPEC.md, and turns the suite from a recorder of divergences into an enforcer
// of the ruled set.
//
// It exits non-zero when:
//
//   - a fixture where the two implementations disagree (different observed
//     error kind, or one opens and the other rejects) is not in
//     manifest.json.divergences            -> unruled divergence;
//   - a ruled divergence is missing its `rule`;
//   - a ruled divergence cites a SPEC section that has no heading in SPEC.md;
//   - a ruled divergence's recorded go/rust kind is outside the fixture's accept
//     set, or a fixture with a multi-kind accept set is not listed;
//   - the manifest carries no `divergence_policy`.
//
// It reports, non-fatally, ruled divergences that are now resolved (both
// implementations agree) and ruled entries that no longer name a fixture.
//
// When the Rust observations are absent (reference not staged) it reports
// rust=skipped and does not fail for that reason alone.
//
// Usage:
//
//	divergence -manifest conformance/manifest.json -spec conformance/SPEC.md \
//	    -go evidence/conformance-go.json [-rust evidence/conformance-rust.json] \
//	    [-json evidence/divergence.json]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ---- inputs -----------------------------------------------------------------

type expected struct {
	ErrorKind       *string  `json:"error_kind"`
	ErrorKindAccept []string `json:"error_kind_accept"`
}

type fixture struct {
	Name     string   `json:"name"`
	Polarity string   `json:"polarity"`
	Expected expected `json:"expected"`
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
	DivergencePolicy string       `json:"divergence_policy"`
	Fixtures         []fixture    `json:"fixtures"`
	Divergences      []divergence `json:"divergences"`
}

// obsResult mirrors the per-fixture observation both verifiers publish.
type obsResult struct {
	Name      string  `json:"name"`
	Polarity  string  `json:"polarity"`
	Opened    bool    `json:"opened"`
	ErrorKind *string `json:"error_kind"`
}

type obsDoc struct {
	Implementation string      `json:"implementation"`
	Fixtures       []obsResult `json:"fixtures"`
}

// ---- output -----------------------------------------------------------------

type entry struct {
	Fixture string `json:"fixture"`
	Go      string `json:"go"`
	Rust    string `json:"rust"`
	Rule    string `json:"rule"`
	Status  string `json:"status"` // ruled | resolved | unruled | missing
}

type summary struct {
	Ruled            int      `json:"ruled"`
	Observed         int      `json:"observed"`
	Unruled          int      `json:"unruled"`
	Resolved         int      `json:"resolved"`
	RuledWithoutRule int      `json:"ruled_without_rule"`
	MissingFixtures  int      `json:"missing_fixture"`
	Rust             string   `json:"rust"` // "ok" | "skipped"
	Source           string   `json:"source"`
	Divergences      []entry  `json:"divergences"`
	Failures         []string `json:"failures"`
}

func main() {
	root := flag.String("root", ".", "conformance directory containing manifest.json and SPEC.md")
	manifestPath := flag.String("manifest", "", "path to manifest.json (default <root>/manifest.json)")
	specPath := flag.String("spec", "", "path to SPEC.md (default <root>/SPEC.md)")
	goPath := flag.String("go", "", "path to the Go verifier JSON observations (required)")
	rustPath := flag.String("rust", "", "path to the Rust verifier JSON observations (optional; absent => rust=skipped)")
	outPath := flag.String("json", "", "write the machine-readable summary JSON here")
	flag.Parse()

	man := *manifestPath
	if man == "" {
		man = filepath.Join(*root, "manifest.json")
	}
	spec := *specPath
	if spec == "" {
		spec = filepath.Join(*root, "SPEC.md")
	}
	if *goPath == "" {
		fmt.Fprintln(os.Stderr, "divergence: -go is required")
		os.Exit(2)
	}

	s, err := compare(man, spec, *goPath, *rustPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "divergence: %v\n", err)
		os.Exit(2)
	}

	if *outPath != "" {
		b, err := json.MarshalIndent(s, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "divergence: marshal: %v\n", err)
			os.Exit(2)
		}
		b = append(b, '\n')
		if err := os.WriteFile(*outPath, b, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "divergence: write %s: %v\n", *outPath, err)
			os.Exit(2)
		}
	}

	for _, e := range s.Divergences {
		fmt.Printf("%-8s %-26s go=%-24s rust=%-24s rule=%s\n", e.Status, e.Fixture, e.Go, e.Rust, e.Rule)
	}
	for _, f := range s.Failures {
		fmt.Fprintf(os.Stderr, "FAIL %s\n", f)
	}
	fmt.Printf("RESULT divergence ruled=%d observed=%d unruled=%d resolved=%d ruled_without_rule=%d missing=%d rust=%s\n",
		s.Ruled, s.Observed, s.Unruled, s.Resolved, s.RuledWithoutRule, s.MissingFixtures, s.Rust)

	if len(s.Failures) > 0 {
		os.Exit(1)
	}
}

func compare(manifestPath, specPath, goPath, rustPath string) (*summary, error) {
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	specText, err := os.ReadFile(specPath)
	if err != nil {
		return nil, fmt.Errorf("read spec: %w", err)
	}
	goDoc, err := readObs(goPath)
	if err != nil {
		return nil, fmt.Errorf("read go observations: %w", err)
	}
	var rustDoc *obsDoc
	rustStatus := "skipped"
	if rustPath != "" {
		if _, statErr := os.Stat(rustPath); statErr == nil {
			rd, err := readObs(rustPath)
			if err != nil {
				return nil, fmt.Errorf("read rust observations: %w", err)
			}
			rustDoc = &rd
			rustStatus = "ok"
		}
	}

	s := &summary{Ruled: len(m.Divergences), Rust: rustStatus, Source: "manifest.json+SPEC.md"}
	fail := func(format string, a ...any) {
		s.Failures = append(s.Failures, fmt.Sprintf(format, a...))
	}

	if strings.TrimSpace(m.DivergencePolicy) == "" {
		fail("manifest: divergence_policy is missing or empty")
	}

	sections := specSections(string(specText))

	fx := make(map[string]fixture, len(m.Fixtures))
	for _, f := range m.Fixtures {
		fx[f.Name] = f
	}

	// --- validate the ruled set --------------------------------------------
	ruled := map[string]divergence{}
	for i, d := range m.Divergences {
		if _, dup := ruled[d.Fixture]; dup {
			fail("manifest: duplicate divergence entry %q", d.Fixture)
		}
		ruled[d.Fixture] = d

		if strings.TrimSpace(d.Rule) == "" {
			s.RuledWithoutRule++
			fail("ruled divergence %q (divergences[%d]) has no rule", d.Fixture, i)
		} else {
			cites := citedSections(d.Rule)
			if len(cites) == 0 {
				fail("ruled divergence %q rule %q cites no SPEC section", d.Fixture, d.Rule)
			}
			for _, sec := range cites {
				if !sections[sec] {
					fail("ruled divergence %q cites SPEC §%s, which has no heading in SPEC.md", d.Fixture, sec)
				}
			}
		}

		f, ok := fx[d.Fixture]
		if !ok {
			s.MissingFixtures++
			continue // reported non-fatally below
		}
		accept := acceptSet(f)
		if !accept[d.Go] {
			fail("ruled divergence %q: recorded go kind %q is not in the fixture's accept set %v", d.Fixture, d.Go, sortedKeys(accept))
		}
		if !accept[d.Rust] {
			fail("ruled divergence %q: recorded rust kind %q is not in the fixture's accept set %v", d.Fixture, d.Rust, sortedKeys(accept))
		}
	}

	// A fixture whose accept set has more than one kind is a ruled divergence and
	// MUST be listed, so a new difference cannot hide inside an accept set.
	for _, f := range m.Fixtures {
		if len(acceptSet(f)) > 1 {
			if _, ok := ruled[f.Name]; !ok {
				fail("fixture %q has a multi-kind accept set but is not listed in divergences", f.Name)
			}
		}
	}

	// --- observe both verifiers --------------------------------------------
	goObs := indexObs(goDoc)
	var rustObs map[string]obsResult
	if rustDoc != nil {
		rustObs = indexObs(*rustDoc)
	}

	disagree := map[string]entry{}
	if rustDoc != nil {
		for _, f := range m.Fixtures {
			g, ok := goObs[f.Name]
			if !ok {
				continue
			}
			r, ok := rustObs[f.Name]
			if !ok {
				continue
			}
			if !sameObservation(g, r) {
				disagree[f.Name] = entry{Fixture: f.Name, Go: kindOrOpen(g), Rust: kindOrOpen(r), Status: "unruled"}
			}
		}
	}
	s.Observed = len(disagree)

	// --- classify every ruled and observed entry ---------------------------
	var names []string
	seen := map[string]bool{}
	for _, d := range m.Divergences {
		if !seen[d.Fixture] {
			seen[d.Fixture] = true
			names = append(names, d.Fixture)
		}
	}
	for n := range disagree {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	sort.Strings(names)

	for _, n := range names {
		if d, ok := ruled[n]; ok {
			e := entry{Fixture: n, Go: d.Go, Rust: d.Rust, Rule: d.Rule}
			if _, present := fx[n]; !present {
				e.Status = "missing"
				s.Divergences = append(s.Divergences, e)
				continue
			}
			switch obs, dis := disagree[n]; {
			case dis:
				e.Go, e.Rust, e.Status = obs.Go, obs.Rust, "ruled"
			case rustDoc == nil:
				e.Status = "ruled" // unverifiable without the Rust side
			default:
				e.Status = "resolved"
				s.Resolved++
			}
			s.Divergences = append(s.Divergences, e)
			continue
		}
		e := disagree[n]
		e.Status = "unruled"
		s.Unruled++
		s.Divergences = append(s.Divergences, e)
		fail("unruled divergence %q: go=%s rust=%s is not in manifest.divergences", n, e.Go, e.Rust)
	}

	return s, nil
}

// ---- helpers ----------------------------------------------------------------

func readObs(path string) (obsDoc, error) {
	var d obsDoc
	raw, err := os.ReadFile(path)
	if err != nil {
		return d, err
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return d, err
	}
	return d, nil
}

func indexObs(d obsDoc) map[string]obsResult {
	m := make(map[string]obsResult, len(d.Fixtures))
	for _, r := range d.Fixtures {
		m[r.Name] = r
	}
	return m
}

// sameObservation reports whether the two verifiers observed the same outcome:
// both opened, or both rejected with the same kind.
func sameObservation(a, b obsResult) bool {
	if a.Opened != b.Opened {
		return false
	}
	return kindEq(a.ErrorKind, b.ErrorKind)
}

func kindEq(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func kindOrOpen(r obsResult) string {
	if r.Opened {
		return "OPEN"
	}
	if r.ErrorKind == nil {
		return "?"
	}
	return *r.ErrorKind
}

func acceptSet(f fixture) map[string]bool {
	set := map[string]bool{}
	if f.Expected.ErrorKind != nil {
		set[*f.Expected.ErrorKind] = true
	}
	for _, k := range f.Expected.ErrorKindAccept {
		set[k] = true
	}
	return set
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// sectionHeading matches a Markdown heading that starts with a dotted section
// number, e.g. "### 7.2 The torn-commit rule".
var sectionHeading = regexp.MustCompile(`(?m)^#{2,6}[ \t]+(\d+(?:\.\d+)*)\b`)

func specSections(spec string) map[string]bool {
	out := map[string]bool{}
	for _, m := range sectionHeading.FindAllStringSubmatch(spec, -1) {
		out[m[1]] = true
	}
	return out
}

// sectionCite matches a "§7.2"-style citation inside a rule string.
var sectionCite = regexp.MustCompile(`§[ \t]*(\d+(?:\.\d+)*)`)

func citedSections(rule string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range sectionCite.FindAllStringSubmatch(rule, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}
