package ekb

// AppendGeneration adds a new committed generation to an existing image,
// manifesting the structures named by profile for the object already stored.
// Earlier bytes are never rewritten and the new commit's prev_generation links to
// the current newest commit, so a torn append can never invalidate it.
//
// source must be the logical content of the stored object (the same core), since
// the appended structures are built against the existing authoritative core.
// Returns the new image bytes and the new generation number.
func AppendGeneration(data []byte, machineID uint32, source []byte, profile Profile) ([]byte, uint64, error) {
	return AppendGenerationAuthenticated(data, machineID, source, profile, nil)
}

// AppendGenerationAuthenticated is AppendGeneration with an opt-in keyed AUTH
// section on the new commit; its MAC covers the whole file prefix, so any prior
// tampering is caught on reopen.
func AppendGenerationAuthenticated(data []byte, machineID uint32, source []byte, profile Profile, key *[32]byte) ([]byte, uint64, error) {
	outcome, err := Scan(data)
	if err != nil {
		return nil, 0, err
	}
	if len(outcome.Commits) == 0 {
		return nil, 0, &Error{Kind: KindRecoveryFailed, Reason: "no valid committed generation to append to"}
	}
	last := outcome.Commits[len(outcome.Commits)-1]
	header := outcome.Header
	manifest, err := CommitManifest(data, &last)
	if err != nil {
		return nil, 0, err
	}
	core := data[outcome.CoreStart:outcome.CoreEnd]
	if header.CoreMachineID != machineID ||
		header.LogicalSymbols != uint64(len(source)) ||
		header.ObjectID != manifest.ObjectID ||
		blake3Hash(core) != manifest.CoreHash {
		return nil, 0, &Error{Kind: KindInvalidArgument, Reason: "appended object does not match the committed object"}
	}
	machine, ok := machineByID(machineID)
	if !ok {
		return nil, 0, &Error{Kind: KindUnknownMachine, MachineID: machineID}
	}
	if err := machine.CheckCore(core, header.LogicalSymbols); err != nil {
		return nil, 0, err
	}
	// The appended object must be the stored one: re-encoding `source` with the
	// named machine must reproduce the committed core exactly.
	newCore, err := machine.Encode(source)
	if err != nil {
		return nil, 0, err
	}
	if blake3Hash(newCore) != manifest.CoreHash {
		return nil, 0, &Error{Kind: KindInvalidArgument, Reason: "appended object does not match the committed object"}
	}

	newGen := last.Generation + 1

	haveHistory := false
	for _, c := range outcome.Commits {
		for _, s := range c.Sections {
			if s.Kind == SectionHistoryFull {
				haveHistory = true
			}
		}
	}

	components, err := accountStructures(header, outcome.Commits, data)
	if err != nil {
		return nil, 0, err
	}

	var sections []pendingSection
	switch profile.Kind {
	case ProfileRoot:
		// nothing new
	case ProfileFull:
		if !haveHistory {
			payload, err := buildHistory(machine, core, header.LogicalSymbols, newGen)
			if err != nil {
				return nil, 0, err
			}
			sections = append(sections, pendingSection{SectionHistoryFull, payload})
			components = append(components, ComponentReport{Name: "history", Kind: "history", Bytes: (header.LogicalSymbols + 1) * 4})
		}
	case ProfileSparse:
		payload, err := buildCheckpoints(machine, core, header.LogicalSymbols, profile.Interval)
		if err != nil {
			return nil, 0, err
		}
		layout, _ := parseCheckpointLayout(payload)
		sections = append(sections, pendingSection{SectionCheckpoints, payload})
		components = append(components, ComponentReport{Name: "checkpoint", Kind: "checkpoint", Bytes: layout.count * uint64(layout.entryLen)})
	default:
		return nil, 0, &Error{Kind: KindInvalidArgument, Reason: "append supports root, full and sparse profiles"}
	}

	record := ManifestRecord{
		Version:    manifestRecordVersion,
		Generation: newGen,
		LogicalLen: header.LogicalSymbols,
		ObjectID:   header.ObjectID,
		MachineID:  machineID,
		CoreHash:   manifest.CoreHash,
		Profiles:   append(append([]string{}, manifest.Profiles...), profile.label()),
		Components: toManifestComponents(components),
	}
	sections = append(sections, pendingSection{SectionManifest, record.encode()})

	// Truncate any torn tail back to the newest valid commit, then append.
	clean := last.End
	out := make([]byte, clean, clean+4096)
	copy(out, data[:clean])
	base := align8(clean)
	out = appendZeros(out, base-clean)
	commit, err := buildCommit(base, out, newGen, last.Generation, sections, key)
	if err != nil {
		return nil, 0, err
	}
	out = append(out, commit...)
	return out, newGen, nil
}

// TruncateToValid returns the longest prefix of data ending at the newest fully
// valid commit, discarding any torn tail.
func TruncateToValid(data []byte) ([]byte, uint64, error) {
	outcome, err := Scan(data)
	if err != nil {
		return nil, 0, err
	}
	if len(outcome.Commits) == 0 {
		return nil, 0, &Error{Kind: KindRecoveryFailed, Reason: "no valid committed generation"}
	}
	last := outcome.Commits[len(outcome.Commits)-1]
	return data[:last.End], last.Generation, nil
}
