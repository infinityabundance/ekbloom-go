// Package provenance records the environment a timing measurement was taken in,
// so a reviewer can tell whether a number was captured on an idle or a contended
// host. It is deliberately dependency-free (standard library only); this file is
// the courts-module copy of github.com/infinityabundance/ekbloom-go/internal/tools/provenance,
// which the separate courts module cannot import. Keep the two in sync.
//
// Every latency/throughput artifact carries a top-level "provenance" object. It
// records the toolchain and process geometry, the container limits, the resolved
// container image digest, and — the point of the exercise — the load average and
// the CPU idle fraction observed at measure time. A high load average on its own
// does not mean the CPU was busy: containers share the host kernel, so
// /proc/loadavg is normally the HOST's, and Linux counts processes blocked on I/O
// (I/O-wait) toward it. cpu_idle_pct is recorded alongside so the two can be read
// together, and each metric's _min is the least-contended estimate.
package provenance

import (
	"bytes"
	"encoding/json"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// note is the honesty paragraph attached to every stamp. It is constant so the
// stamp is byte-stable apart from the genuinely measured fields.
const note = "Captured in-container at measure time. /proc/loadavg inside the container is normally the HOST's (containers share the kernel), and Linux counts processes blocked on I/O (I/O-wait) toward load average, so a high loadavg can appear even when the CPU is idle; cpu_idle_pct = (idle+iowait)/total from /proc/stat is recorded alongside so the two can be read together. Each metric's _min is the least-contended run's estimate, and the _min-vs-median gap is the contention signal."

// LoadAvg is the 1/5/15-minute load average from /proc/loadavg.
type LoadAvg struct {
	One     float64 `json:"one"`
	Five    float64 `json:"five"`
	Fifteen float64 `json:"fifteen"`
}

// Provenance is the self-describing environment stamp attached to every timing
// artifact. Field order is fixed so the stamp is deterministic apart from the
// measured values.
type Provenance struct {
	Go               string  `json:"go"`
	GOOS             string  `json:"goos"`
	GOARCH           string  `json:"goarch"`
	GOMAXPROCS       int     `json:"gomaxprocs"`
	Nproc            int     `json:"nproc"`
	Kernel           string  `json:"kernel"`
	ContainerImage   string  `json:"container_image"`
	ContainerImageID string  `json:"container_image_id"`
	MemoryLimit      string  `json:"memory_limit"`
	CPULimit         string  `json:"cpu_limit"`
	PidsLimit        string  `json:"pids_limit"`
	LoadAvg          LoadAvg `json:"loadavg"`
	CPUIdlePct       float64 `json:"cpu_idle_pct"`
	RunQueue         int     `json:"runqueue"`
	TimestampUTC     string  `json:"timestamp_utc"`
	EKBRuns          int     `json:"ekb_runs"`
	Note             string  `json:"note"`
}

// Runs returns R = EKB_RUNS, matching internal/tools/stat.Runs exactly: EKB_RUNS if
// set to a positive integer, else 3 when CI is set, else 5. It is duplicated here
// (rather than imported) so this package stays self-contained and copies verbatim
// into the courts module.
func Runs() int {
	def := 5
	if os.Getenv("CI") != "" {
		def = 3
	}
	if v := os.Getenv("EKB_RUNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// Current captures the provenance of the running process/container now. The
// container limits and image come from the environment the wrapper scripts inject
// (EKB_CONTAINER_IMAGE, EKB_CONTAINER_IMAGE_ID, EKB_LIMIT_MEM, EKB_LIMIT_CPUS,
// EKB_LIMIT_PIDS); they are empty when the process runs outside a wrapper.
func Current() Provenance {
	one, five, fifteen, runq := loadavg()
	return Provenance{
		Go:               runtime.Version(),
		GOOS:             runtime.GOOS,
		GOARCH:           runtime.GOARCH,
		GOMAXPROCS:       runtime.GOMAXPROCS(0),
		Nproc:            runtime.NumCPU(),
		Kernel:           readProcTrim("/proc/sys/kernel/osrelease"),
		ContainerImage:   os.Getenv("EKB_CONTAINER_IMAGE"),
		ContainerImageID: os.Getenv("EKB_CONTAINER_IMAGE_ID"),
		MemoryLimit:      os.Getenv("EKB_LIMIT_MEM"),
		CPULimit:         os.Getenv("EKB_LIMIT_CPUS"),
		PidsLimit:        os.Getenv("EKB_LIMIT_PIDS"),
		LoadAvg:          LoadAvg{One: one, Five: five, Fifteen: fifteen},
		CPUIdlePct:       CPUIdlePct(),
		RunQueue:         runq,
		TimestampUTC:     time.Now().UTC().Format(time.RFC3339),
		EKBRuns:          Runs(),
		Note:             note,
	}
}

// Compact returns the provenance as a single-line JSON object, for embedding in
// another JSON document (the RocksDB C++ bridge does this) or in a comment line.
func (p Provenance) Compact() []byte {
	b, err := json.Marshal(p)
	if err != nil {
		return []byte("{}")
	}
	return b
}

// Stamp inserts Current() as the "provenance" key of the top-level JSON object in
// b. Every existing byte of b is preserved — only the closing '}' gains a
// preceding comma — so deterministic fields remain byte-identical across captures.
// Non-object input (empty, array, scalar) is returned unchanged. The result ends
// in a newline, matching the artifacts' previous "append(out, '\n')" convention.
func Stamp(b []byte) []byte { return StampWith(b, Current()) }

// StampWith is Stamp with an explicit Provenance, for callers that already
// captured one and for tests.
func StampWith(b []byte, p Provenance) []byte {
	inner := bytes.TrimRight(b, " \t\r\n")
	if len(inner) == 0 || inner[len(inner)-1] != '}' {
		return b
	}
	body := bytes.TrimRight(inner[:len(inner)-1], " \t\r\n")
	var out []byte
	if len(body) == 1 && body[0] == '{' {
		out = append(out, '{', '\n', ' ', ' ')
	} else {
		out = append(out, body...)
		out = append(out, ',', '\n', ' ', ' ')
	}
	out = append(out, `"provenance": `...)
	pj, err := json.MarshalIndent(p, "  ", "  ")
	if err != nil {
		return b
	}
	out = append(out, pj...)
	out = append(out, '\n', '}', '\n')
	return out
}

// CPUIdlePct samples the first ("cpu ") line of /proc/stat and returns
// 100*(idle+iowait)/total, i.e. the fraction of the interval the CPUs were not
// executing. I/O-wait is counted as idle: it is exactly the case a high load
// average with a busy-looking host is made of. Returns 0 if /proc/stat is
// unavailable.
func CPUIdlePct() float64 {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)[1:] // drop the "cpu" label
		vals := make([]float64, 0, len(fields))
		for _, f := range fields {
			v, err := strconv.ParseFloat(f, 64)
			if err != nil {
				break
			}
			vals = append(vals, v)
		}
		// user nice system idle iowait irq softirq steal ... — need at least idle+iowait.
		if len(vals) < 5 {
			return 0
		}
		var total float64
		for _, v := range vals {
			total += v
		}
		if total == 0 {
			return 0
		}
		return 100 * (vals[3] + vals[4]) / total
	}
	return 0
}

// loadavg parses /proc/loadavg. The fourth field is "running/total" processes; its
// numerator is the run queue length.
func loadavg() (one, five, fifteen float64, runq int) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0, 0
	}
	f := strings.Fields(string(b))
	if len(f) >= 3 {
		one, _ = strconv.ParseFloat(f[0], 64)
		five, _ = strconv.ParseFloat(f[1], 64)
		fifteen, _ = strconv.ParseFloat(f[2], 64)
	}
	if len(f) >= 4 {
		if i := strings.IndexByte(f[3], '/'); i > 0 {
			runq, _ = strconv.Atoi(f[3][:i])
		}
	}
	return one, five, fifteen, runq
}

// readProcTrim reads a small /proc file and trims surrounding whitespace, or
// returns "" if it is unavailable.
func readProcTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
