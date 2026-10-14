package ekb

import "hash/crc32"

// Verify performs a structural integrity check of an in-memory `.ekb` image.
//
// Every section payload is checksummed (CRC-32), the commit footer is validated,
// the manifest linkage is checked, and the visible core is verified against the
// manifest's BLAKE3 `core_hash`. On failure it returns a typed error; a
// detected corruption in a located structure is returned as
// *Error{Kind: KindCorrupt, Section: <name>, Offset: <absolute byte offset>},
// localized to the containing structure (at most one section/frame, which the
// writer bounds to <= 4096 bytes for every structure it emits).
//
// Inert alignment padding is never interpreted, so a flip confined to padding is
// not (and must not be) reported — matching the reference behavior.
func Verify(data []byte) error {
	header, err := decodeGenesisHeader(data)
	if err != nil {
		return err
	}
	coreEnd := coreOffset + header.CoreLen
	if coreEnd < coreOffset {
		return overflow("core extent")
	}
	if uint64(len(data)) < coreEnd {
		return truncated(coreEnd, uint64(len(data)))
	}
	total := uint64(len(data))
	pos := align8(coreEnd)
	var prev uint64
	any := false
	for pos < total {
		commit, corruptErr, err := walkCommit(data, pos, prev)
		if err != nil {
			return err
		}
		if corruptErr != nil {
			return corruptErr
		}
		prev = commit.Generation
		any = true
		pos = commit.End
	}
	if !any {
		return &Error{Kind: KindRecoveryFailed, Reason: "no valid committed generation"}
	}
	// Cross-check the visible core against the newest manifest's core hash.
	return verifyCoreHash(data, header, coreOffset, coreEnd)
}

// walkCommit parses one commit, returning a localized Corrupt error on the first
// failing checksum instead of the generic ChecksumMismatch.
func walkCommit(data []byte, start, expectedPrev uint64) (ScannedCommit, error, error) {
	var commit ScannedCommit
	total := uint64(len(data))
	cur := start
	var sections []ScannedSection
	var footerStart uint64
	for {
		headerEnd := cur + sectionHeaderLen
		if headerEnd < cur {
			return commit, nil, overflow("section header extent")
		}
		if total < headerEnd {
			return commit, nil, truncated(headerEnd, total)
		}
		header, err := decodeSectionHeader(data[cur:headerEnd])
		if err != nil {
			return commit, nil, err
		}
		if header.Offset != headerEnd {
			return commit, nil, badSection("section offset does not follow its header")
		}
		payloadEnd := headerEnd + header.Length
		if payloadEnd < headerEnd {
			return commit, nil, overflow("section payload extent")
		}
		if total < payloadEnd {
			return commit, nil, truncated(payloadEnd, total)
		}
		if crc32.ChecksumIEEE(data[headerEnd:payloadEnd]) != header.Checksum {
			return commit, corrupt(header.Kind.Name(), headerEnd), nil
		}
		sections = append(sections, ScannedSection{
			Kind:          header.Kind,
			Generation:    header.Generation,
			HeaderOffset:  cur,
			PayloadOffset: headerEnd,
			PayloadLen:    header.Length,
		})
		cur = align8(payloadEnd)
		if header.Kind == SectionManifest {
			if auth, next, ok := scanAuthOpt(data, cur, total); ok {
				sections = append(sections, auth)
				cur = next
			}
			footerStart = cur
			break
		}
	}
	footerEnd := footerStart + footerLen
	if footerEnd < footerStart {
		return commit, nil, overflow("footer extent")
	}
	if total < footerEnd {
		return commit, nil, truncated(footerEnd, total)
	}
	footer, err := decodeCommitFooter(data[footerStart:footerEnd])
	if err != nil {
		if e, ok := err.(*Error); ok && e.Kind == KindChecksumMismatch {
			return commit, corrupt("commit-footer", footerStart), nil
		}
		return commit, nil, err
	}
	manifestIdx := -1
	for i := len(sections) - 1; i >= 0; i-- {
		if sections[i].Kind == SectionManifest {
			manifestIdx = i
			break
		}
	}
	if manifestIdx < 0 {
		return commit, nil, badSection("commit has no manifest section")
	}
	manifest := sections[manifestIdx]
	if footer.ManifestOffset != manifest.HeaderOffset || footer.ManifestLen != manifest.PayloadLen {
		return commit, nil, badSection("footer manifest pointer disagrees with section")
	}
	manifestPayload := data[manifest.PayloadOffset : manifest.PayloadOffset+manifest.PayloadLen]
	h := blake3Hash(manifestPayload)
	if footer.ManifestHash != [16]byte(h[:16]) {
		return commit, corrupt("manifest", manifest.PayloadOffset), nil
	}
	record, err := decodeManifestRecord(manifestPayload)
	if err != nil {
		return commit, nil, err
	}
	if record.Generation != footer.Generation {
		return commit, nil, badSection("manifest generation disagrees with footer")
	}
	commit = ScannedCommit{
		Generation:     footer.Generation,
		PrevGeneration: footer.PrevGeneration,
		ManifestOffset: footer.ManifestOffset,
		ManifestLen:    footer.ManifestLen,
		ManifestHash:   footer.ManifestHash,
		Start:          start,
		End:            footerEnd,
		Sections:       sections,
	}
	return commit, nil, nil
}

// VerifyCoreHash re-derives the newest manifest and confirms the visible core
// matches its BLAKE3 core hash. Returns a localized Corrupt error on mismatch.
func verifyCoreHash(data []byte, header GenesisHeader, coreStart, coreEnd uint64) error {
	_, err := decodeGenesisHeader(data)
	if err != nil {
		return err
	}
	// Find the newest manifest by scanning.
	outcome, err := Scan(data)
	if err != nil {
		return err
	}
	if len(outcome.Commits) == 0 {
		return &Error{Kind: KindRecoveryFailed, Reason: "no valid committed generation"}
	}
	last := outcome.Commits[len(outcome.Commits)-1]
	manifest, err := CommitManifest(data, &last)
	if err != nil {
		return err
	}
	core := data[coreStart:coreEnd]
	if blake3Hash(core) != manifest.CoreHash {
		return corrupt("core", coreStart)
	}
	return nil
}

// VerifyDeep performs Verify plus the core-hash cross-check, returning a
// localized *Error{Kind: KindCorrupt} on the first divergence.
func VerifyDeep(data []byte) error {
	header, err := decodeGenesisHeader(data)
	if err != nil {
		return err
	}
	if err := Verify(data); err != nil {
		return err
	}
	coreEnd := coreOffset + header.CoreLen
	return verifyCoreHash(data, header, coreOffset, coreEnd)
}
