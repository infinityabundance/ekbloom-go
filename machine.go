package ekbloom

import "github.com/infinityabundance/ekbloom-go/internal/blake3"

// Machine identifiers.
const (
	MachineNL2     uint32 = 1
	MachineRans0   uint32 = 2
	MachineStore   uint32 = 3
	MachineRans0I  uint32 = 4
	MachinePack    uint32 = 5
	MachineSeg     uint32 = 6
	MachineRans0W  uint32 = 7
	MachineRans0IL uint32 = 8
	MachineDict    uint32 = 9
)

// maxLanes is the number of independent rANS lanes an interleaved machine may
// expose (mirrors the Rust `MAX_LANES`).
const maxLanes = 8

// sbox is the fixed bijective byte permutation S(v) = v ^ (v<<1) ^ (v<<3).
var sbox [256]byte

func init() {
	for i := 0; i < 256; i++ {
		v := byte(i)
		sbox[i] = v ^ (v << 1) ^ (v << 3)
	}
}

// blake3Hash is the unkeyed BLAKE3 hash used throughout the format.
func blake3Hash(b []byte) [32]byte { return blake3.Sum(b) }

// blake3KeyedHash is the keyed BLAKE3 hash used by the AUTH section.
func blake3KeyedHash(key [32]byte, b []byte) [32]byte { return blake3.SumKeyed(key, b) }

// Snapshot is the complete resumable operational state Omega = (x, p[, lanes]).
type Snapshot struct {
	State   uint32
	Cursor  uint64
	Lanes   [maxLanes]uint32
	Lane    uint8
	Cursors [maxLanes]uint64
}

// Machine is a reproducible, resumable symbol-generating machine. A machine's
// core semantics are identified by its (ID, core version).
//
// An external contributor adding machine 10 implements this interface, registers
// it with RegisterMachine, and satisfies the contract in docs/MACHINE_CONTRACT.md.
type Machine interface {
	// ID is the stable machine identifier stored in the `.ekb` header.
	ID() uint32
	// Init returns the legal initial executable state for core.
	Init(core []byte) (Snapshot, error)
	// Reveal reveals one symbol and advances s by one step.
	Reveal(core []byte, s *Snapshot) (byte, error)
	// Peek returns the next symbol without advancing.
	Peek(core []byte, s *Snapshot) (byte, error)
	// Encode encodes a source into its visible core.
	Encode(source []byte) ([]byte, error)
	// CheckCore validates that core can produce logicalLen symbols.
	CheckCore(core []byte, logicalLen uint64) error
}

var registry = map[uint32]Machine{}

// RegisterMachine installs a machine implementation under its ID. It panics only
// on a nil machine or an ID of 0 (programmer error, not untrusted input). It
// exists so machine 10 (see docs/MACHINE_CONTRACT.md) can be added without
// editing the core.
func RegisterMachine(m Machine) {
	if m == nil {
		panic("ekbloom: RegisterMachine(nil)")
	}
	registry[m.ID()] = m
}

func init() {
	RegisterMachine(nl2{})
	RegisterMachine(rans0{})
	RegisterMachine(store{})
	RegisterMachine(rans0i{})
	RegisterMachine(pack{})
	RegisterMachine(seg{})
	RegisterMachine(rans0w{})
	RegisterMachine(rans0il{})
	RegisterMachine(dictMachine{})
}

// MachineByID returns the registered machine for id (and whether it exists).
func MachineByID(id uint32) (Machine, bool) { return machineByID(id) }

func machineByID(id uint32) (Machine, bool) {
	m, ok := registry[id]
	return m, ok
}

// prepared is a validated, reusable decoder context for one core, built once so
// a bulk decode does not re-parse the model per symbol.
type prepared interface {
	reveal(core []byte, s *Snapshot) (byte, error)
	peek(core []byte, s *Snapshot) (byte, error)
}

// prepare builds the decoder context for machine m over core.
func prepare(m Machine, core []byte) (prepared, error) {
	switch m.ID() {
	case MachineRans0:
		return ransContextFromCore(core)
	case MachineRans0W:
		return ransWContextFromCore(core)
	case MachineRans0I:
		return ransIContextFromCore(core)
	case MachineRans0IL:
		return ilContextFromCore(core)
	case MachinePack:
		return packContextFromCore(core)
	case MachineSeg:
		return segContextFromCore(core)
	case MachineDict:
		return dictContextFromCore(core)
	default:
		return delegated{m}, nil
	}
}

// delegated is the prepared context for the cheap machines (1 and 3), whose step
// is already a single computation.
type delegated struct{ m Machine }

func (d delegated) reveal(core []byte, s *Snapshot) (byte, error) { return d.m.Reveal(core, s) }
func (d delegated) peek(core []byte, s *Snapshot) (byte, error)   { return d.m.Peek(core, s) }

// DecodeCore decodes a machine core to its logical bytes, using the machine
// registered for machineID. It is the inverse of Machine.Encode and is the entry
// point for storing a core yourself (e.g. a value-log adapter) and decoding it
// later. Malformed input is a typed error; it never panics.
func DecodeCore(machineID uint32, core []byte, logicalLen uint64) ([]byte, error) {
	m, ok := machineByID(machineID)
	if !ok {
		return nil, &Error{Kind: KindUnknownMachine, MachineID: machineID}
	}
	return decodeAll(m, core, logicalLen)
}

// decodeAll decodes every logical symbol of core into a fresh slice, using a
// prepared context so the model is parsed once.
func decodeAll(m Machine, core []byte, logicalLen uint64) ([]byte, error) {
	if err := m.CheckCore(core, logicalLen); err != nil {
		return nil, err
	}
	if err := checkDefaultAllocation(logicalLen); err != nil {
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
	out := make([]byte, logicalLen)
	for i := uint64(0); i < logicalLen; i++ {
		b, err := ctx.reveal(core, &s)
		if err != nil {
			return nil, err
		}
		out[i] = b
	}
	return out, nil
}
