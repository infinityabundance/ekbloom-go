package ekbloom

import "sync"

// Reader is a validated, read-only view over an in-memory `.ekb` file.
//
// A Reader is immutable after Open; ByteAt and ReadAt are safe for concurrent
// use by many goroutines (the lazily materialised symbol array is built once
// under a sync.Once). It never panics on malformed input: every failure is a
// typed *Error.
type Reader struct {
	data     []byte
	header   GenesisHeader
	commits  []ScannedCommit
	commit   ScannedCommit
	manifest ManifestRecord
	core     []byte
	machine  Machine
	logical  uint64

	components    []ComponentReport
	physicalBytes uint64
	profiles      []string
	generation    uint64
	estimate      uint64
	authenticated bool

	hasHistory       bool
	cursorPositional bool
	checkpointIvls   []uint32

	fast *fastpath

	hotOnce sync.Once
	hot     []byte
	hotErr  error

	packOnce sync.Once
	packCtx  *packContext
	packErr  error
	segOnce  sync.Once
	segCtx   *segContext
	segErr   error
}

// Open validates and opens an in-memory `.ekb` file, returning a Reader.
func Open(data []byte) (*Reader, error) {
	outcome, err := Scan(data)
	if err != nil {
		return nil, err
	}
	if len(outcome.Commits) == 0 {
		if outcome.tornErr != nil {
			return nil, &Error{Kind: KindRecoveryFailed, Reason: outcome.tornErr.Error()}
		}
		return nil, &Error{Kind: KindRecoveryFailed, Reason: "no valid committed generation"}
	}
	commit := outcome.Commits[len(outcome.Commits)-1]
	manifest, err := CommitManifest(data, &commit)
	if err != nil {
		return nil, err
	}
	header := outcome.Header
	if manifest.ObjectID != header.ObjectID {
		return nil, badSection("manifest object_id disagrees with the header")
	}
	if manifest.MachineID != header.CoreMachineID {
		return nil, badSection("manifest machine_id disagrees with the header")
	}
	if manifest.LogicalLen != header.LogicalSymbols {
		return nil, badSection("manifest logical_len disagrees with the header")
	}
	core := data[outcome.CoreStart:outcome.CoreEnd]
	if blake3Hash(core) != manifest.CoreHash {
		return nil, &Error{Kind: KindChecksumMismatch, Section: "core"}
	}
	machine, ok := machineByID(header.CoreMachineID)
	if !ok {
		return nil, &Error{Kind: KindUnknownMachine, MachineID: header.CoreMachineID}
	}
	if err := machine.CheckCore(core, header.LogicalSymbols); err != nil {
		return nil, err
	}
	components, err := accountStructures(header, outcome.Commits, data)
	if err != nil {
		return nil, err
	}
	var physical uint64
	for _, c := range components {
		physical += c.Bytes
	}

	r := &Reader{
		data:          data,
		header:        header,
		commits:       outcome.Commits,
		commit:        commit,
		manifest:      manifest,
		core:          core,
		machine:       machine,
		logical:       header.LogicalSymbols,
		components:    components,
		physicalBytes: physical,
		profiles:      manifest.Profiles,
		generation:    manifest.Generation,
		cursorPositional: header.CoreMachineID == MachineNL2 || header.CoreMachineID == MachineStore ||
			header.CoreMachineID == MachinePack || header.CoreMachineID == MachineSeg,
	}
	for _, c := range outcome.Commits {
		for _, s := range c.Sections {
			switch s.Kind {
			case SectionHistoryFull:
				r.hasHistory = true
			case SectionCheckpoints:
				if cp, err := parseCheckpointLayout(data[s.PayloadOffset : s.PayloadOffset+s.PayloadLen]); err == nil {
					r.checkpointIvls = append(r.checkpointIvls, cp.interval)
				}
			}
		}
	}
	primary := rootProfile()
	if len(manifest.Profiles) > 0 {
		if p, ok := parseProfileLabel(manifest.Profiles[0]); ok {
			primary = p
		}
	}
	r.estimate = EstimatePhysicalBytesForCore(header.CoreLen, r.logical, primary)
	r.fast = parseFastPaths(header, outcome.Commits, data, manifest.CoreHash)
	return r, nil
}

// LogicalLen is the number of logical symbols N.
func (r *Reader) LogicalLen() uint64 { return r.logical }

// Generation is the newest committed generation.
func (r *Reader) Generation() uint64 { return r.generation }

// MachineID is the core machine identifier.
func (r *Reader) MachineID() uint32 { return r.header.CoreMachineID }

// CoreBytes returns the visible core (do not mutate).
func (r *Reader) CoreBytes() []byte { return r.core }

// Header returns the decoded genesis header.
func (r *Reader) Header() GenesisHeader { return r.header }

// Manifest returns the newest manifest record.
func (r *Reader) Manifest() ManifestRecord { return r.manifest }

// PhysicalBytes is the total manifested physical bytes (core plus every
// structure), equal to the Rust `inspect_file(..).physical_bytes`.
func (r *Reader) PhysicalBytes() uint64 { return r.physicalBytes }

// VisibleBytes is the visible core length V.
func (r *Reader) VisibleBytes() uint64 { return uint64(len(r.core)) }

// Estimate is the static upper-bound estimate of the physical bytes for this
// object's committed profile.
func (r *Reader) Estimate() uint64 { return r.estimate }

// EstimateForProfile is the static upper-bound estimate for an arbitrary profile.
func (r *Reader) EstimateForProfile(p Profile) uint64 {
	return EstimatePhysicalBytesForCore(uint64(len(r.core)), r.logical, p)
}

// Report returns the full manifestation accounting.
func (r *Reader) Report() Report {
	visible := uint64(len(r.core))
	ratio := 0.0
	if visible != 0 {
		ratio = float64(r.physicalBytes) / float64(visible)
	}
	concealed := uint64(0)
	if r.physicalBytes > visible {
		concealed = r.physicalBytes - visible
	}
	profs := make([]string, len(r.profiles))
	copy(profs, r.profiles)
	comps := make([]ComponentReport, len(r.components))
	copy(comps, r.components)
	return Report{
		LogicalLen:     r.logical,
		VisibleBytes:   visible,
		PhysicalBytes:  r.physicalBytes,
		ConcealedBytes: concealed,
		ExpansionRatio: ratio,
		Generation:     r.generation,
		Profiles:       profs,
		Components:     comps,
	}
}

// ByteAt returns the logical symbol at position off. It never panics on a
// malformed request; an out-of-range offset is a typed OutOfBounds error.
//
// Reads are served, in order, by the exact path bloom, the machine-1 history, the
// nearest checkpoint restart, the directly-addressable machines (3/5/6), and
// finally the lazily materialised symbol array — mirroring the reference's
// fastest-correct-path selection.
func (r *Reader) ByteAt(off uint64) (byte, error) {
	if off >= r.logical {
		return 0, outOfBounds(off, 1, r.logical)
	}
	if r.directlyAddressable() {
		return r.symbolAtDirect(off)
	}
	if out, ok := r.readPathBloom(off, off+1); ok {
		return out[0], nil
	}
	if b, ok := r.byteAtHistory(off); ok {
		return b, nil
	}
	if out, ok := r.readCheckpoint(off, off+1); ok {
		return out[0], nil
	}
	hot, err := r.symbols()
	if err != nil {
		return 0, err
	}
	return hot[off], nil
}

// ReadAt returns length logical symbols starting at off.
func (r *Reader) ReadAt(off, length uint64) ([]byte, error) {
	end := off + length
	if end < off {
		return nil, overflow("read range")
	}
	if end > r.logical {
		return nil, outOfBounds(off, length, r.logical)
	}
	if length == 0 {
		return []byte{}, nil
	}
	if r.directlyAddressable() {
		if r.machine.ID() == MachineStore {
			out := make([]byte, length)
			copy(out, r.core[off:end])
			return out, nil
		}
		out := make([]byte, length)
		for i := uint64(0); i < length; i++ {
			b, err := r.symbolAtDirect(off + i)
			if err != nil {
				return nil, err
			}
			out[i] = b
		}
		return out, nil
	}
	if out, ok := r.readPathBloom(off, end); ok {
		return out, nil
	}
	if out, ok := r.readHistory(off, end); ok {
		return out, nil
	}
	if out, ok := r.readCheckpoint(off, end); ok {
		return out, nil
	}
	hot, err := r.symbols()
	if err != nil {
		return nil, err
	}
	out := make([]byte, length)
	copy(out, hot[off:end])
	return out, nil
}

// directlyAddressable reports whether the machine supports O(1) positional
// access without materialising the whole decode (machines 3, 5 and 6).
func (r *Reader) directlyAddressable() bool {
	switch r.machine.ID() {
	case MachineStore, MachinePack, MachineSeg:
		return true
	}
	return false
}

func (r *Reader) symbolAtDirect(off uint64) (byte, error) {
	switch r.machine.ID() {
	case MachineStore:
		return r.core[off], nil
	case MachinePack:
		r.packOnce.Do(func() { r.packCtx, r.packErr = packContextFromCore(r.core) })
		if r.packErr != nil {
			return 0, r.packErr
		}
		return r.packCtx.byteAt(r.core, off)
	case MachineSeg:
		r.segOnce.Do(func() { r.segCtx, r.segErr = segContextFromCore(r.core) })
		if r.segErr != nil {
			return 0, r.segErr
		}
		return r.segCtx.byteAt(r.core, off)
	}
	return 0, badSection("machine is not directly addressable")
}

// symbols lazily materialises the full logical decode exactly once.
func (r *Reader) symbols() ([]byte, error) {
	r.hotOnce.Do(func() {
		r.hot, r.hotErr = decodeAll(r.machine, r.core, r.logical)
	})
	return r.hot, r.hotErr
}

// WorstCaseStepsFor returns an upper bound on the number of machine positioning
// steps plus output reveals a ReadAt(off, length) may perform through the
// reader's certified path:
//
//   - a full history on a cursor==position machine gives the state directly
//     (0 positioning steps);
//   - otherwise a checkpoint bounds the replay to less than its interval;
//   - with neither, the replay is from genesis (off steps).
//
// The output reveals are always exactly length.
func (r *Reader) WorstCaseStepsFor(off, length uint64) (uint64, error) {
	end := off + length
	if end < off {
		return 0, overflow("worst-case range")
	}
	if end > r.logical {
		return 0, outOfBounds(off, length, r.logical)
	}
	var positioning uint64
	switch {
	case r.fast.pathHot != nil:
		// A certified path bloom serves any position with no positioning steps.
		positioning = 0
	case r.hasHistory && r.cursorPositional:
		positioning = 0
	case len(r.checkpointIvls) > 0:
		best := off
		for _, k := range r.checkpointIvls {
			if k == 0 {
				continue
			}
			d := off % uint64(k)
			if d < best {
				best = d
			}
		}
		positioning = best
	default:
		positioning = off
	}
	return positioning + length, nil
}
