package ekb

import (
	"sort"

	"github.com/infinityabundance/ekbloom-go/internal/blake3"
)

// Tile-directory kinds (wire codes).
const (
	TileFlat TileDirectoryKind = iota
	TileTwoLevelRadix
)

// TileDirectoryKind selects the tile-set directory layout.
type TileDirectoryKind int

const tileABSENT = uint32(0xFFFFFFFF)

func tileLenOf(tile uint64, shift uint8, logicalLen uint64) uint64 {
	start := tile << shift
	if start >= logicalLen {
		return 0
	}
	size := uint64(1) << shift
	if size > logicalLen-start {
		return logicalLen - start
	}
	return size
}

// WriteBodyTiles writes a `TILES` body: the exact authoritative decode of each
// named tile, plus a Flat directory, byte-compatible with the Rust
// `TileSet::to_bytes`. Tiles are materialised ascending; each is an exact copy of
// the authoritative decode of its range.
func WriteBodyTiles(machineID uint32, source []byte, tileShift uint8, presentTiles []uint64) ([]byte, error) {
	machine, ok := machineByID(machineID)
	if !ok {
		return nil, &Error{Kind: KindUnknownMachine, MachineID: machineID}
	}
	if tileShift < 1 || tileShift > 30 {
		return nil, &Error{Kind: KindInvalidArgument, Reason: "tile_shift out of range 1..=30"}
	}
	core, err := machine.Encode(source)
	if err != nil {
		return nil, err
	}
	logicalLen := uint64(len(source))
	decoded, err := decodeAll(machine, core, logicalLen)
	if err != nil {
		return nil, err
	}
	size := uint64(1) << tileShift
	nTiles := (logicalLen + size - 1) / size

	// Normalise the present set: ascending, unique, in range.
	set := map[uint64]bool{}
	for _, t := range presentTiles {
		if t >= nTiles {
			return nil, &Error{Kind: KindInvalidArgument, Reason: "present tile index out of range"}
		}
		set[t] = true
	}
	order := make([]int, 0, len(set))
	for t := range set {
		order = append(order, int(t))
	}
	sort.Ints(order)

	flat := make([]uint32, nTiles)
	for i := range flat {
		flat[i] = tileABSENT
	}
	var data []byte
	hasher := blake3.NewHasher()
	hasher.Write([]byte("ekbloom.tiles.v1"))
	hasher.Write([]byte{tileShift, byte(TileFlat)})
	var mb [4]byte
	mb[0] = byte(machineID)
	mb[1] = byte(machineID >> 8)
	mb[2] = byte(machineID >> 16)
	mb[3] = byte(machineID >> 24)
	hasher.Write(mb[:])
	hasher.Write(le64(logicalLen))
	for _, ti := range order {
		tile := uint64(ti)
		ln := tileLenOf(tile, tileShift, logicalLen)
		start := tile << tileShift
		off := uint32(len(data))
		flat[ti] = off
		data = append(data, decoded[start:start+ln]...)
		hasher.Write(le64(tile))
		hasher.Write(le64(ln))
		hasher.Write(decoded[start : start+ln])
	}
	digest := hasher.Sum()
	coreHash := blake3Hash(core)

	payload := make([]byte, 0, 96+8+len(flat)*4+8+len(data))
	payload = append(payload, []byte("EKBTILES")...)
	payload = append(payload, 1) // version
	payload = append(payload, tileShift)
	payload = append(payload, byte(TileFlat))
	payload = append(payload, 0) // reserved
	payload = append(payload, mb[:]...)
	payload = appendU64(payload, logicalLen)
	payload = appendU64(payload, uint64(len(order)))
	payload = append(payload, coreHash[:]...)
	payload = append(payload, digest[:]...)
	// Flat directory: u64 length ++ entries.
	payload = appendU64(payload, uint64(len(flat)))
	for _, v := range flat {
		payload = appendU32(payload, v)
	}
	payload = appendU64(payload, uint64(len(data)))
	payload = append(payload, data...)

	components := []ComponentReport{
		{Name: "core", Kind: "core", Bytes: uint64(len(core))},
		{Name: "tiles", Kind: "tiles", Bytes: uint64(len(data)) + uint64(len(flat))*4},
	}
	manifest := ManifestRecord{
		Version:    manifestRecordVersion,
		Generation: 1,
		LogicalLen: logicalLen,
		ObjectID:   deriveObjectID(coreHash),
		MachineID:  machineID,
		CoreHash:   coreHash,
		Profiles:   []string{"tiles(shift=" + uitoa(uint64(tileShift)) + ")"},
		Components: toManifestComponents(components),
	}
	sections := []pendingSection{
		{SectionTiles, payload},
		{SectionManifest, manifest.encode()},
	}

	header := GenesisHeader{
		FormatMajor:    formatMajor,
		FormatMinor:    formatMinor,
		ObjectID:       deriveObjectID(coreHash),
		LogicalSymbols: logicalLen,
		LogicalBytes:   logicalLen,
		CoreLen:        uint64(len(core)),
		CoreMachineID:  machineID,
		CoreVersion:    coreVersionCurrent,
	}
	headerBytes := header.Encode()
	coreEnd := uint64(coreOffset) + uint64(len(core))
	base := align8(coreEnd)
	out := make([]byte, 0, int(base)+len(payload)+1024)
	out = append(out, headerBytes[:]...)
	out = append(out, core...)
	out = appendZeros(out, base-coreEnd)
	commit, err := buildCommit(base, out, 1, 0, sections, nil)
	if err != nil {
		return nil, err
	}
	out = append(out, commit...)
	return out, nil
}

func le64(v uint64) []byte {
	var b [8]byte
	for i := 0; i < 8; i++ {
		b[i] = byte(v >> (8 * i))
	}
	return b[:]
}
