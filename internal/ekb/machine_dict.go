package ekb

import "encoding/binary"

// dictMachine is OrANS-DICT (machine_id = 9): the shared-dictionary record machine.
type dictMachine struct{}

func (dictMachine) ID() uint32 { return MachineDict }

type dictContext struct {
	rans         *ransContext
	dictID       [16]byte
	initialState uint32
	recordOffset int
	logicalLen   uint64
}

func dictContextFromCore(core []byte) (*dictContext, error) {
	if len(core) < dictFramePrefixBytes {
		return nil, truncated(dictFramePrefixBytes, uint64(len(core)))
	}
	var dictID [16]byte
	copy(dictID[:], core[0:16])
	dictLen := int(binary.LittleEndian.Uint32(core[16:]))
	recordOffset := dictFramePrefixBytes + dictLen

	var dict Dictionary
	if dictLen == 0 {
		d, ok := LookupDictionary(dictID)
		if !ok {
			return nil, badSection("unknown or unregistered dictionary id")
		}
		dict = d
	} else {
		if recordOffset > len(core) {
			return nil, truncated(uint64(recordOffset), uint64(len(core)))
		}
		embedded, err := DictionaryFromBytes(core[dictFramePrefixBytes:recordOffset])
		if err != nil {
			return nil, err
		}
		if embedded.id != dictID {
			return nil, badSection("embedded dictionary id does not match the core's dictionary id")
		}
		dict = embedded
	}

	recordEnd := recordOffset + dictRecordHeaderBytes
	if recordEnd > len(core) {
		return nil, truncated(uint64(recordEnd), uint64(len(core)))
	}
	logicalLen := uint64(binary.LittleEndian.Uint32(core[recordOffset:]))
	state := binary.LittleEndian.Uint32(core[recordOffset+4:])
	if !validState(state) {
		return nil, badSection("dictionary record state out of range")
	}
	if err := checkDefaultAllocation(logicalLen); err != nil {
		return nil, err
	}
	header := dict.modelHeader(state)
	rans, err := ransContextFromCore(header)
	if err != nil {
		return nil, err
	}
	return &dictContext{
		rans:         rans,
		dictID:       dictID,
		initialState: state,
		recordOffset: recordOffset,
		logicalLen:   logicalLen,
	}, nil
}

func (c *dictContext) headerBytes() uint64 {
	return uint64(c.recordOffset + dictRecordHeaderBytes)
}

func (c *dictContext) genesis() Snapshot {
	var s Snapshot
	s.State = c.initialState
	s.Cursor = c.headerBytes()
	return s
}

func (c *dictContext) reveal(core []byte, s *Snapshot) (byte, error) {
	return c.rans.reveal(core, s)
}

func (c *dictContext) peek(core []byte, s *Snapshot) (byte, error) {
	return c.rans.peek(core, s)
}

func (dictMachine) Init(core []byte) (Snapshot, error) {
	c, err := dictContextFromCore(core)
	if err != nil {
		return Snapshot{}, err
	}
	return c.genesis(), nil
}

func (dictMachine) Reveal(core []byte, s *Snapshot) (byte, error) {
	c, err := dictContextFromCore(core)
	if err != nil {
		return 0, err
	}
	return c.reveal(core, s)
}

func (dictMachine) Peek(core []byte, s *Snapshot) (byte, error) {
	c, err := dictContextFromCore(core)
	if err != nil {
		return 0, err
	}
	return c.peek(core, s)
}

// Encode trains a dictionary from source and embeds it (self-contained).
func (dictMachine) Encode(source []byte) ([]byte, error) {
	d, err := TrainDictionary([][]byte{source})
	if err != nil {
		return nil, err
	}
	record, err := d.EncodeRecord(source)
	if err != nil {
		return nil, err
	}
	return frameEmbedded(d, record), nil
}

func (dictMachine) CheckCore(core []byte, logicalLen uint64) error {
	c, err := dictContextFromCore(core)
	if err != nil {
		return err
	}
	if logicalLen != c.logicalLen {
		return badSection("dictionary core declares a different symbol count")
	}
	cap := ransStepCap(core)
	s := c.genesis()
	for steps := uint64(0); steps < logicalLen; steps++ {
		if steps >= cap {
			return &Error{Kind: KindNonTermination}
		}
		if _, err := c.reveal(core, &s); err != nil {
			return err
		}
	}
	return nil
}
