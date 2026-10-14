// Command ekbloom is a small CLI over the pure-Go EKBLOOM codec. It is the entry
// point of the minimal container image and a convenient way to inspect, read and
// verify `.ekb`/`.stream` files without writing Go.
//
// Usage:
//
//	ekbloom inspect  <file.ekb>                 JSON accounting report
//	ekbloom byte     <file.ekb> <offset>        print one logical symbol (hex + dec)
//	ekbloom read     <file.ekb> <offset> <len>  write the logical range to stdout
//	ekbloom verify   <file.ekb>                 exit 0 if clean; else print Corrupt{...}
//	ekbloom stream   <file.stream>              JSON stream manifest
//	ekbloom version
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	ekbloom "github.com/infinityabundance/ekbloom-go"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "inspect":
		err = cmdInspect(os.Args[2:])
	case "byte":
		err = cmdByte(os.Args[2:])
	case "read":
		err = cmdRead(os.Args[2:])
	case "verify":
		err = cmdVerify(os.Args[2:])
	case "stream":
		err = cmdStream(os.Args[2:])
	case "version":
		fmt.Println("ekbloom-go 0.1.0")
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `ekbloom <command> [args]

commands:
  inspect  <file.ekb>                 JSON accounting report
  byte     <file.ekb> <offset>        print one logical symbol
  read     <file.ekb> <offset> <len>  write the logical range to stdout
  verify   <file.ekb>                 verify integrity (exit 0 if clean)
  stream   <file.stream>              JSON stream manifest
  version
`)
}

func openReader(path string) (*ekbloom.Reader, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ekbloom.Open(data)
}

type componentJSON struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Bytes uint64 `json:"bytes"`
}

func cmdInspect(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("inspect needs a file")
	}
	r, err := openReader(args[0])
	if err != nil {
		return err
	}
	rep := r.Report()
	comps := make([]componentJSON, 0, len(rep.Components))
	for _, c := range rep.Components {
		comps = append(comps, componentJSON{c.Name, c.Kind, c.Bytes})
	}
	out := struct {
		MachineID      uint32          `json:"machine_id"`
		LogicalLen     uint64          `json:"logical_len"`
		VisibleBytes   uint64          `json:"visible_bytes"`
		PhysicalBytes  uint64          `json:"physical_bytes"`
		ConcealedBytes uint64          `json:"concealed_bytes"`
		ExpansionRatio float64         `json:"expansion_ratio"`
		Generation     uint64          `json:"generation"`
		Profiles       []string        `json:"profiles"`
		Estimate       uint64          `json:"estimate"`
		Components     []componentJSON `json:"components"`
	}{
		r.MachineID(), r.LogicalLen(), rep.VisibleBytes, rep.PhysicalBytes,
		rep.ConcealedBytes, rep.ExpansionRatio, rep.Generation, rep.Profiles,
		r.Estimate(), comps,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func cmdByte(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("byte needs <file> <offset>")
	}
	off, err := strconv.ParseUint(args[1], 10, 64)
	if err != nil {
		return err
	}
	r, err := openReader(args[0])
	if err != nil {
		return err
	}
	b, err := r.ByteAt(off)
	if err != nil {
		return err
	}
	fmt.Printf("offset=%d value=0x%02x (%d)\n", off, b, b)
	return nil
}

func cmdRead(args []string) error {
	if len(args) < 3 {
		return fmt.Errorf("read needs <file> <offset> <len>")
	}
	off, err := strconv.ParseUint(args[1], 10, 64)
	if err != nil {
		return err
	}
	length, err := strconv.ParseUint(args[2], 10, 64)
	if err != nil {
		return err
	}
	r, err := openReader(args[0])
	if err != nil {
		return err
	}
	out, err := r.ReadAt(off, length)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(out)
	return err
}

func cmdVerify(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("verify needs a file")
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	if err := ekbloom.VerifyDeep(data); err != nil {
		if section, offset, ok := ekbloom.CorruptDetail(err); ok {
			fmt.Printf("CORRUPT section=%q offset=%d\n", section, offset)
		}
		return err
	}
	fmt.Println("OK")
	return nil
}

func cmdStream(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("stream needs a file")
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	r, err := ekbloom.OpenStream(data)
	if err != nil {
		return err
	}
	sh := r.SourceHash()
	out := struct {
		LogicalLen     uint64 `json:"logical_len"`
		BlockBytes     uint32 `json:"block_bytes"`
		BlockCount     uint64 `json:"block_count"`
		ContainerBytes uint64 `json:"container_bytes"`
		PayloadBytes   uint64 `json:"payload_bytes"`
		SourceHash     string `json:"source_hash"`
	}{
		r.Len(), r.BlockBytes(), r.BlockCount(), r.ContainerBytes(), r.PayloadBytes(),
		fmt.Sprintf("%x", sh),
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
