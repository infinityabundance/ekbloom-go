package ekb

import "bytes"

// OpenAuthenticated opens and validates an image, then requires a valid keyed
// AUTH tag in the newest commit. A missing, stripped or mismatching tag is a
// typed error — it never falls back to an unkeyed read.
func OpenAuthenticated(data []byte, key [32]byte) (*Reader, error) {
	r, err := Open(data)
	if err != nil {
		return nil, err
	}
	if err := verifyAuthSection(data, r.commit, key); err != nil {
		return nil, err
	}
	r.authenticated = true
	return r, nil
}

// VerifyAuthenticated checks integrity (VerifyDeep) and the keyed AUTH tag of
// the newest commit.
func VerifyAuthenticated(data []byte, key [32]byte) error {
	if err := VerifyDeep(data); err != nil {
		return err
	}
	outcome, err := Scan(data)
	if err != nil {
		return err
	}
	if len(outcome.Commits) == 0 {
		return &Error{Kind: KindRecoveryFailed, Reason: "no valid committed generation"}
	}
	return verifyAuthSection(data, outcome.Commits[len(outcome.Commits)-1], key)
}

// Authenticated reports whether this Reader was opened with OpenAuthenticated.
func (r *Reader) Authenticated() bool { return r.authenticated }

// verifyAuthSection recomputes the AUTH MAC over data[0..auth_header_offset) and
// compares it to the stored 32-byte tag.
func verifyAuthSection(data []byte, commit ScannedCommit, key [32]byte) error {
	for _, s := range commit.Sections {
		if s.Kind != SectionAuth {
			continue
		}
		if s.PayloadLen != 32 {
			return badSection("AUTH section payload is not 32 bytes")
		}
		covered := blake3Hash(data[:s.HeaderOffset])
		expected := blake3KeyedHash(key, covered[:])
		got := data[s.PayloadOffset : s.PayloadOffset+32]
		if !bytes.Equal(got, expected[:]) {
			return badSection("AUTH tag mismatch")
		}
		return nil
	}
	return badSection("no AUTH section present")
}
