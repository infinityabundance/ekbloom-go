package ekbloom

// Path-bloom writer parity: emit an exact `P_D[j]` path bloom (the headline
// `path` profile) plus its certification record, byte-compatible with the Rust
// reference (`PathBloom::build` + `make_certificate`).

const pathPayloadPrefixLen = 2 + 8 + 8 // depth u16, logical_len u64, nblocks u64
const shellHeaderLenBytes = 74

var supportedDepths = map[uint16]bool{1: true, 2: true, 4: true, 8: true, 16: true, 32: true}

func certificateFieldsPath() [4]string {
	return [4]string{"block-ordinal", "block-index", "exact-by-construction", "every-logical-position"}
}

// machineStates returns the state log x_0 ..= x_N for machine m over core.
func machineStates(m Machine, core []byte, logicalLen uint64) ([]uint32, error) {
	if err := m.CheckCore(core, logicalLen); err != nil {
		return nil, err
	}
	ctx, err := prepare(m, core)
	if err != nil {
		return nil, err
	}
	s, err := m.Init(core)
	if err != nil {
		return nil, err
	}
	states := make([]uint32, logicalLen+1)
	states[0] = s.State
	for i := uint64(0); i < logicalLen; i++ {
		if _, err := ctx.reveal(core, &s); err != nil {
			return nil, err
		}
		states[i+1] = s.State
	}
	return states, nil
}

// buildPathBloom writes the path-bloom shell and its certificate payloads.
func buildPathBloom(m Machine, core []byte, logicalLen uint64, depth uint16, generation uint64, coreHash [32]byte) (shell, cert []byte, err error) {
	if !supportedDepths[depth] {
		return nil, nil, &Error{Kind: KindBadDepth, Offset: uint64(depth)}
	}
	hot, err := decodeAll(m, core, logicalLen)
	if err != nil {
		return nil, nil, err
	}
	states, err := machineStates(m, core, logicalLen)
	if err != nil {
		return nil, nil, err
	}
	d := uint64(depth)
	nblocks := (logicalLen + d - 1) / d

	// Inner payload: prefix ++ hot ++ cold.
	payload := make([]byte, 0, pathPayloadPrefixLen+len(hot)+int(nblocks)*4)
	payload = appendU16(payload, depth)
	payload = appendU64(payload, logicalLen)
	payload = appendU64(payload, nblocks)
	payload = append(payload, hot...)
	for j := uint64(0); j < nblocks; j++ {
		end := d*(j+1) - 1
		if d*(j+1) > logicalLen {
			end = logicalLen
		}
		payload = appendU32(payload, states[end])
	}

	// Shell: kind u32, depth u16, generation u64, logical_len u64, entries u64,
	// status u32, core_hash [32], payload_len u64, payload.
	shell = make([]byte, 0, shellHeaderLenBytes+len(payload))
	shell = appendU32(shell, 3) // BloomKind::Path
	shell = appendU16(shell, depth)
	shell = appendU64(shell, generation)
	shell = appendU64(shell, logicalLen)
	shell = appendU64(shell, nblocks)
	shell = appendU32(shell, 1) // Certification::Certified
	shell = append(shell, coreHash[:]...)
	shell = appendU64(shell, uint64(len(payload)))
	shell = append(shell, payload...)

	// Certificate over the shell.
	bloomHash := blake3Hash(shell)
	fields := certificateFieldsPath()
	var receipt []byte
	receipt = appendU32(receipt, 3)
	receipt = appendU16(receipt, depth)
	receipt = appendU32(receipt, m.ID())
	receipt = appendU64(receipt, generation)
	receipt = appendU32(receipt, 1)
	for _, s := range fields {
		receipt = appendU64(receipt, uint64(len(s)))
		receipt = append(receipt, s...)
	}
	receipt = append(receipt, coreHash[:]...)
	receipt = append(receipt, bloomHash[:]...)
	receiptHash := blake3Hash(receipt)
	cert = append(append([]byte(nil), receipt...), receiptHash[:]...)
	return shell, cert, nil
}
