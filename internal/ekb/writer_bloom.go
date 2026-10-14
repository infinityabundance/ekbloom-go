package ekb

import "sort"

// Universal (`T_D[x]`) and regional bloom writers, byte-compatible with the Rust
// reference (`build_global_shell_with` / `build_regional_with` + `make_certificate`).

const (
	universeKeys    = 65536
	presentBytes    = universeKeys / 8 // 8192
	globalPrefixLen = 2 + 8 + 8 + 4
	regionPrefixLen = 2 + 8 + 8 + 8 + 8 + 4 + 8
)

func keyFromState(x uint32) uint16 { return uint16(x) }

// buildCertificate serializes a Certificate (receipt_bytes ++ receipt_hash).
func buildCertificate(kindCode uint32, depth uint16, machineID uint32, generation uint64, status uint32, fields [4]string, coreHash, bloomHash [32]byte) []byte {
	var r []byte
	r = appendU32(r, kindCode)
	r = appendU16(r, depth)
	r = appendU32(r, machineID)
	r = appendU64(r, generation)
	r = appendU32(r, status)
	for _, s := range fields {
		r = appendU64(r, uint64(len(s)))
		r = append(r, s...)
	}
	r = append(r, coreHash[:]...)
	r = append(r, bloomHash[:]...)
	rh := blake3Hash(r)
	return append(r, rh[:]...)
}

func bloomShell(kindCode uint32, depth uint16, generation, logicalLen, entries uint64, status uint32, coreHash [32]byte, payload []byte) []byte {
	out := make([]byte, 0, shellHeaderLenBytes+len(payload))
	out = appendU32(out, kindCode)
	out = appendU16(out, depth)
	out = appendU64(out, generation)
	out = appendU64(out, logicalLen)
	out = appendU64(out, entries)
	out = appendU32(out, status)
	out = append(out, coreHash[:]...)
	out = appendU64(out, uint64(len(payload)))
	return append(out, payload...)
}

// buildUniversalShell builds a `Universal` shell (status Unverified) and its
// certificate.
func buildUniversalShell(m Machine, core []byte, logicalLen uint64, depth uint16, generation uint64, coreHash [32]byte) (shell, cert []byte, err error) {
	if !supportedDepths[depth] {
		return nil, nil, &Error{Kind: KindBadDepth, Offset: uint64(depth)}
	}
	decoded, err := decodeAll(m, core, logicalLen)
	if err != nil {
		return nil, nil, err
	}
	states, err := machineStates(m, core, logicalLen)
	if err != nil {
		return nil, nil, err
	}
	d := int(depth)
	symbols := make([]byte, universeKeys*d) // slot -> D symbols
	next := make([]uint32, universeKeys)
	present := make([]bool, universeKeys)
	var windows int
	if logicalLen >= uint64(depth) {
		windows = int(logicalLen - uint64(depth) + 1)
	}
	for i := 0; i < windows; i++ {
		key := keyFromState(states[i])
		if present[key] {
			continue
		}
		present[key] = true
		copy(symbols[int(key)*d:], decoded[i:i+d])
		next[key] = states[i+d]
	}

	payload := make([]byte, 0, globalPrefixLen+presentBytes+universeKeys*(d+4))
	payload = appendU16(payload, depth)
	payload = appendU64(payload, universeKeys)
	payload = appendU64(payload, presentBytes)
	payload = appendU32(payload, 0) // Certification::Unverified
	mask := make([]byte, presentBytes)
	for i, p := range present {
		if p {
			mask[i/8] |= 1 << uint(i%8)
		}
	}
	payload = append(payload, mask...)
	for slot := 0; slot < universeKeys; slot++ {
		payload = append(payload, symbols[slot*d:slot*d+d]...)
		payload = appendU32(payload, next[slot])
	}

	shell = bloomShell(0, depth, generation, 0, universeKeys, 0, coreHash, payload)
	bloomHash := blake3Hash(shell)
	fields := [4]string{"x16", "none", "state-universe-scan", "observed-window-states"}
	cert = buildCertificate(0, depth, m.ID(), generation, 0, fields, coreHash, bloomHash)
	return shell, cert, nil
}

// buildRegionalShells builds one `Regional` shell per region plus its certificate.
func buildRegionalShells(m Machine, core []byte, logicalLen uint64, depth uint16, regionBytes uint32, generation uint64, coreHash [32]byte) (shells [][]byte, certs [][]byte, err error) {
	if !supportedDepths[depth] {
		return nil, nil, &Error{Kind: KindBadDepth, Offset: uint64(depth)}
	}
	if regionBytes == 0 {
		return nil, nil, &Error{Kind: KindInvalidArgument, Reason: "region bytes must be non-zero"}
	}
	if logicalLen == 0 {
		return nil, nil, nil
	}
	decoded, err := decodeAll(m, core, logicalLen)
	if err != nil {
		return nil, nil, err
	}
	states, err := machineStates(m, core, logicalLen)
	if err != nil {
		return nil, nil, err
	}
	rb := uint64(regionBytes)
	d := uint64(depth)
	nregions := (logicalLen + rb - 1) / rb
	fields := [4]string{"x16-within-region", "region-local", "region-exhaustive", "every-region-state"}

	var tableOffset uint64
	for region := uint64(0); region < nregions; region++ {
		start := region * rb
		end := start + rb
		if end > logicalLen {
			end = logicalLen
		}
		length := end - start
		var windows uint64
		if length >= d {
			windows = length - d + 1
		}
		type keyState struct {
			out      []byte // D symbols ++ next u32 (LE)
			pos      uint64
			conflict bool
		}
		byKey := map[uint16]*keyState{}
		for i := start; i < start+windows; i++ {
			key := keyFromState(states[i])
			o := make([]byte, 0, int(d)+4)
			o = append(o, decoded[i:i+d]...)
			o = appendU32(o, states[i+d])
			ks, ok := byKey[key]
			if !ok {
				byKey[key] = &keyState{out: o, pos: i}
			} else if !ks.conflict && !equalBytes(ks.out, o) {
				ks.conflict = true
			}
		}
		keys := make([]int, 0, len(byKey))
		for k := range byKey {
			keys = append(keys, int(k))
		}
		sort.Ints(keys)
		entries := uint64(len(keys))
		tableLen := entries * (d + 6)
		conflicts := uint64(0)
		for _, k := range keys {
			if byKey[uint16(k)].conflict {
				conflicts++
			}
		}
		status := uint32(1) // Certified
		if conflicts > 0 {
			status = 2 // Rejected
		}
		payload := make([]byte, 0, regionPrefixLen+int(tableLen))
		payload = appendU16(payload, depth)
		payload = appendU64(payload, start)
		payload = appendU64(payload, length)
		payload = appendU64(payload, tableOffset)
		payload = appendU64(payload, entries)
		payload = appendU32(payload, status)
		payload = appendU64(payload, tableLen)
		for _, k := range keys {
			payload = appendU16(payload, uint16(k))
			payload = append(payload, byKey[uint16(k)].out...)
		}
		shell := bloomShell(2, depth, generation, length, entries, status, coreHash, payload)
		bloomHash := blake3Hash(shell)
		cert := buildCertificate(2, depth, m.ID(), generation, status, fields, coreHash, bloomHash)
		shells = append(shells, shell)
		certs = append(certs, cert)
		tableOffset += tableLen
	}
	return shells, certs, nil
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// buildPathCertificate builds the path certificate (shared by the path writer).
func buildPathCertificate(m Machine, depth uint16, generation uint64, status uint32, coreHash, bloomHash [32]byte) []byte {
	return buildCertificate(3, depth, m.ID(), generation, status, certificateFieldsPath(), coreHash, bloomHash)
}
