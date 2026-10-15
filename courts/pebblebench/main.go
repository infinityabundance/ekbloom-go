// Command pebblebench is the EKBLOOM machine-9 Pebble integration-depth study.
//
// It complements the point-read story in courts/pebble with the four LSM-shaped
// scenarios a storage reviewer actually asks about, all on a real Pebble DB
// whose values are machine-9 cores (dictionary reference) decoded with a single
// reusable DictDecoder into a caller-reused buffer:
//
//   - scan:      db.NewIter over a key range, decode + verify every value.
//   - ingest:    build an offline SSTable (sstable.NewWriter, values = cores)
//     and db.Ingest it, then verify every ingested key reads back.
//   - compaction: db.Flush + db.Compact over the whole range; measure wall time,
//     on-disk size before/after, and derive write amplification from
//     Pebble's own metrics.
//   - cache pressure: reopen the same DB with a deliberately small bounded block
//     cache (pebble.NewCache) and replay the 80/20 Gets against a
//     working set larger than the cache, reporting cache hits/misses
//     and the point-read p50/p99/p99.9 under pressure vs the warm case.
//
// The two datasets interleave on even/odd keys over [0, 2*records) so the
// ingested SSTable overlaps the flushed dataset in key range. That is what makes
// the forced compaction rewrite bytes instead of performing a trivial move.
//
// Every round-trip is verified; any mismatch aborts the run (FATAL). Nothing is
// fabricated: every number comes from a real measurement or a Pebble metric.
//
// Honest boundary. bytes_per_record is the *codec payload only* — the machine-9
// core — and excludes Pebble's keys, sequence numbers, checksums, Bloom filters,
// WAL and LSM overhead, so the on-disk footprint is larger. Pebble additionally
// applies its own block compression (snappy by default) on top of the cores.
//
// Output contract:
//   - stdout: exactly one JSON object (machine-readable evidence).
//   - stderr: a human-readable table plus round-trip diagnostics.
//   - file:   ../evidence/pebble-integration.prom (Prometheus-style exposition).
//
// Usage: go run ./pebblebench
package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"text/tabwriter"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/objstorage/objstorageprovider"
	"github.com/cockroachdb/pebble/sstable"
	"github.com/cockroachdb/pebble/vfs"
	ekbloom "github.com/infinityabundance/ekbloom-go"
	"github.com/infinityabundance/ekbloom-go/courts/internal/provenance"
	"github.com/infinityabundance/ekbloom-go/courts/internal/stats"
	"github.com/infinityabundance/ekbloom-go/courts/internal/workload"
)

const (
	records     = 20000
	recordBytes = workload.RecordBytes
	gets        = 50000
	trainFrac   = 4
	// keyspace is the interleaved key range: dataset 1 owns even keys, dataset 2
	// owns odd keys, so their ranges overlap and the forced compaction must
	// rewrite bytes rather than trivially move a file.
	keyspace = 2 * records
	// pressureCacheBytes is the deliberately small block cache used for the
	// cache-pressure handle. The live working set (~2.5 MB of cores) does not fit.
	pressureCacheBytes = 128 << 10
	// warmWarmupOps is the steady-state warm-up before each timed read phase, so
	// the warm and pressure phases are compared at the same point.
	warmWarmupOps = 2000
	// pebbleDefaultCacheBytes is the size of Pebble's default block cache, reported
	// for the warm handle (Options.Cache == nil -> 8 MiB).
	pebbleDefaultCacheBytes = 8 << 20
)

var decodeDst = make([]byte, recordBytes)

// check aborts the run on an unexpected error.
func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// report is the machine-readable stdout artifact. Field order fixes the JSON key
// order. Latency fields (scan p99 + point-read p50/p99/p99.9) are the median across
// R = EKB_RUNS runs; ingest/compaction are one-time wall-clock operations.
type report struct {
	Records           int              `json:"records"`
	RecordBytes       int              `json:"record_bytes"`
	Gets              int              `json:"gets"`
	BytesPerRecord    float64          `json:"bytes_per_record"`
	DictBytes         uint64           `json:"dict_bytes"`
	DecodeAllocsPerOp float64          `json:"decode_allocs_per_op"`
	Scan              scanReport       `json:"scan"`
	Ingest            ingestReport     `json:"ingest"`
	Compaction        compactionReport `json:"compaction"`
	PointReadWarm     pointReport      `json:"point_read_warm"`
	PointReadPressure pointReport      `json:"point_read_pressure"`
	Runs              int              `json:"runs"`
	Statistic         string           `json:"statistic"`
	Note              string           `json:"note"`
}

type scanReport struct {
	Keys        int     `json:"keys"`
	NsPerKey    float64 `json:"ns_per_key"`
	MBPerS      float64 `json:"mb_per_s"`
	P99Ns       float64 `json:"p99_ns"`
	Mismatches  int     `json:"mismatches"`
	NsPerKeyMin float64 `json:"ns_per_key_min"`
	NsPerKeyMax float64 `json:"ns_per_key_max"`
	MBPerSMin   float64 `json:"mb_per_s_min"`
	MBPerSMax   float64 `json:"mb_per_s_max"`
	P99Min      float64 `json:"p99_ns_min"`
	P99Max      float64 `json:"p99_ns_max"`
}

type ingestReport struct {
	Records            int     `json:"records"`
	LogicalBytes       int     `json:"logical_bytes"`
	SSTableBytes       int64   `json:"sstable_bytes"`
	BuildMs            float64 `json:"build_ms"`
	IngestMs           float64 `json:"ingest_ms"`
	TotalMs            float64 `json:"total_ms"`
	MBPerS             float64 `json:"mb_per_s"`
	PostReadMismatches int     `json:"post_read_mismatches"`
}

type compactionReport struct {
	FlushMs              float64 `json:"flush_ms"`
	CompactMs            float64 `json:"compact_ms"`
	DBBytesBeforeFlush   int64   `json:"db_bytes_before_flush"`
	DBBytesAfterFlush    int64   `json:"db_bytes_after_flush"`
	DBBytesAfterCompact  int64   `json:"db_bytes_after_compact"`
	DBBytesFinal         int64   `json:"db_bytes_final"`
	FlushSSTBytes        uint64  `json:"flush_sst_bytes"`
	CompactSSTBytes      uint64  `json:"compact_sst_bytes"`
	CompactBytesRead     uint64  `json:"compact_bytes_read"`
	SSTBytesWritten      uint64  `json:"sst_bytes_written"`
	WALBytesWritten      uint64  `json:"wal_bytes_written"`
	LogicalBytes         uint64  `json:"logical_bytes"`
	CoreBytes            uint64  `json:"core_bytes"`
	WriteAmplification   float64 `json:"write_amplification"`
	WriteAmpStored       float64 `json:"write_amplification_stored"`
	PostSampleMismatches int     `json:"post_sample_point_mismatches"`
	PostScanKeys         int     `json:"post_scan_keys"`
	PostScanMismatches   int     `json:"post_scan_mismatches"`
}

type pointReport struct {
	Gets        int     `json:"gets"`
	CacheBytes  int64   `json:"cache_bytes"`
	P50Ns       float64 `json:"p50_ns"`
	P99Ns       float64 `json:"p99_ns"`
	P999Ns      float64 `json:"p999_ns"`
	CacheHits   int64   `json:"cache_hits"`
	CacheMisses int64   `json:"cache_misses"`
	HitRate     float64 `json:"hit_rate"`
	Mismatches  int     `json:"mismatches"`
	P50Min      float64 `json:"p50_ns_min"`
	P50Max      float64 `json:"p50_ns_max"`
	P99Min      float64 `json:"p99_ns_min"`
	P99Max      float64 `json:"p99_ns_max"`
	P999Min     float64 `json:"p999_ns_min"`
	P999Max     float64 `json:"p999_ns_max"`
}

func main() {
	// --- corpus + shared dictionary -----------------------------------------
	s1 := make([][]byte, records)
	for i := range s1 {
		s1[i] = workload.Record(i)
	}
	s2 := make([][]byte, records)
	for i := range s2 {
		s2[i] = workload.Record(records + 7 + i) // a distinct second dataset
	}
	dict, err := ekbloom.TrainDictionary(s1[:records/trainFrac])
	check(err)
	dictBytes := dict.PhysicalBytes()

	dec, err := ekbloom.NewDictDecoder(dict)
	check(err)

	// srcFor maps an interleaved key back to its original logical bytes.
	srcFor := func(idx uint64) ([]byte, bool) {
		if idx >= keyspace {
			return nil, false
		}
		if idx%2 == 0 {
			return s1[idx/2], true
		}
		return s2[idx/2], true
	}

	dir, err := os.MkdirTemp("", "ekbloom-pebble-integration-")
	check(err)
	defer os.RemoveAll(dir)
	dbPath := filepath.Join(dir, "db")

	// Automatic compactions are disabled so the forced db.Compact measurement is
	// attributable to exactly one compaction (otherwise Pebble's background
	// picker merges the two L0 files as soon as they overlap).
	db, err := pebble.Open(dbPath, &pebble.Options{DisableAutomaticCompactions: true})
	check(err)

	// --- dataset 1: values are machine-9 cores on even keys ------------------
	var kb [8]byte
	var coreBytes uint64
	for i, v := range s1 {
		core, err := dict.EncodeRecord(v)
		check(err)
		coreBytes += uint64(len(core))
		binary.BigEndian.PutUint64(kb[:], uint64(2*i))
		check(db.Set(kb[:], core, pebble.NoSync))
	}
	bytesPerRecord := float64(coreBytes) / records

	// Steady-state decode allocations: the reusable decoder writes into a reused
	// buffer, so this is the documented 0-allocation path.
	binary.BigEndian.PutUint64(kb[:], 0) // key 0 (dataset 1, record 0)
	core0, closer0, err := db.Get(kb[:])
	check(err)
	decodeAllocs := testing.AllocsPerRun(1000, func() {
		_, _ = dec.DecodeInto(decodeDst, core0)
	})
	closer0.Close()

	// --- scenario 1: scan (median-of-runs) -----------------------------------
	runs := stats.Runs()
	var scanKeys, scanMismatches int
	scanNsPerKey := make([]float64, 0, runs)
	scanMBsV := make([]float64, 0, runs)
	scanP99V := make([]int64, 0, runs)
	for run := 0; run < runs; run++ {
		k, elapsed, durs, mm := scanRange(db, dec, 0, keyspace, srcFor)
		scanKeys, scanMismatches = k, mm
		sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
		scanNsPerKey = append(scanNsPerKey, float64(elapsed.Nanoseconds())/float64(k))
		scanMBsV = append(scanMBsV, float64(k*recordBytes)/1e6/elapsed.Seconds())
		scanP99V = append(scanP99V, stats.Pct(durs, 990))
	}
	scanNsKey := stats.Collect(scanNsPerKey)
	scanMBsS := stats.Collect(scanMBsV)
	scanP99 := stats.Collect(scanP99V)

	// --- scenario 2: flush dataset 1, then ingest a foreign SSTable ----------
	// Flushing first puts dataset 1 in L0 so the (overlapping) ingested SSTable
	// lands beside it in L0, forcing a real merge when we compact.
	dbBytesBeforeFlush := dirSize(dbPath)
	tFlush := time.Now()
	check(db.Flush())
	flushElapsed := time.Since(tFlush)
	dbBytesAfterFlush := dirSize(dbPath)

	sstPath := filepath.Join(dir, "ingest.sst")
	tBuild := time.Now()
	buildCores := writeSSTable(sstPath, s2, dict)
	buildElapsed := time.Since(tBuild)
	sstInfo, err := os.Stat(sstPath)
	check(err)
	sstBytes := sstInfo.Size()
	coreBytes += buildCores

	tIngest := time.Now()
	check(db.Ingest([]string{sstPath}))
	ingestElapsed := time.Since(tIngest)
	ingestLogical := records * recordBytes
	// Verify every ingested key reads back with the exact original bytes.
	_, ingestedMismatches, _ := verifyRange(db, dec, 0, keyspace, srcFor)

	// --- scenario 3: flush + force a compaction ------------------------------
	// (The memtable is already empty — dataset 1 was flushed above and ingest
	// writes an SSTable directly — so this post-ingest Flush is a no-op; we call
	// it to make the LSM state explicit before compacting.)
	check(db.Flush())

	var lo, hi [8]byte
	binary.BigEndian.PutUint64(lo[:], 0)
	binary.BigEndian.PutUint64(hi[:], uint64(keyspace))
	m1 := db.Metrics()
	flushSST, compact0 := sstWritten(m1)
	read0 := bytesRead(m1)
	tCompact := time.Now()
	check(db.Compact(lo[:], hi[:], true))
	compactElapsed := time.Since(tCompact)
	dbBytesAfterCompact := dirSize(dbPath)

	// Write amplification from Pebble's own metrics. The flush bytes are
	// cumulative (dataset 1); the compaction bytes are the delta across the
	// forced db.Compact so they are attributable to it alone.
	m2 := db.Metrics()
	_, compactAll := sstWritten(m2)
	compactSST := compactAll - compact0
	compactRead := bytesRead(m2) - read0
	sstWrittenTotal := flushSST + compactAll
	walWritten := m2.WAL.BytesWritten
	logicalBytes := uint64(2*records) * recordBytes

	// Re-verify a sample of point reads (both datasets) and a full scan.
	postSampleMismatches := sampleVerify(db, dec, srcFor)
	postScanKeys, _, _, postScanMismatches := scanRange(db, dec, 0, keyspace, srcFor)

	// --- scenario 4a: warm point reads (default cache) -----------------------
	// The 80/20 trace over dataset 1 keys (an existing point-read story). The
	// p50/p99/p99.9 are the median across R = EKB_RUNS runs.
	trace := buildTrace(records)
	runGets(db, dec, trace[:warmWarmupOps], s1) // warm-up; not measured
	warmP50v := make([]int64, 0, runs)
	warmP99v := make([]int64, 0, runs)
	warmP999v := make([]int64, 0, runs)
	warmHitsV := make([]int64, 0, runs)
	warmMissV := make([]int64, 0, runs)
	warmMismatches := 0
	for run := 0; run < runs; run++ {
		h0, m0 := cacheStats(db)
		warmDurs, mm := runGets(db, dec, trace, s1)
		h1, m1 := cacheStats(db)
		warmMismatches += mm
		sort.Slice(warmDurs, func(i, j int) bool { return warmDurs[i] < warmDurs[j] })
		warmP50v = append(warmP50v, stats.Pct(warmDurs, 500))
		warmP99v = append(warmP99v, stats.Pct(warmDurs, 990))
		warmP999v = append(warmP999v, stats.Pct(warmDurs, 999))
		warmHitsV = append(warmHitsV, h1-h0)
		warmMissV = append(warmMissV, m1-m0)
	}
	warmP50 := stats.Collect(warmP50v)
	warmP99 := stats.Collect(warmP99v)
	warmP999 := stats.Collect(warmP999v)
	warmHits := stats.Collect(warmHitsV)
	warmMiss := stats.Collect(warmMissV)

	check(db.Close())
	// After Close, Pebble has unlinked the compaction's obsolete inputs, so this is
	// the true SST footprint on disk (the in-compaction measurement above may
	// still see files pending deletion).
	dbBytesFinal := dirSize(dbPath)

	// --- scenario 4b: cache pressure (small bounded block cache) -------------
	pcache := pebble.NewCache(pressureCacheBytes)
	defer pcache.Unref()
	dbP, err := pebble.Open(dbPath, &pebble.Options{Cache: pcache, DisableAutomaticCompactions: true})
	check(err)
	runGets(dbP, dec, trace[:warmWarmupOps], s1) // same warm-up
	pressP50v := make([]int64, 0, runs)
	pressP99v := make([]int64, 0, runs)
	pressP999v := make([]int64, 0, runs)
	pressHitsV := make([]int64, 0, runs)
	pressMissV := make([]int64, 0, runs)
	pressMismatches := 0
	for run := 0; run < runs; run++ {
		h0, m0 := cacheStats(dbP)
		pressDurs, mm := runGets(dbP, dec, trace, s1)
		h1, m1 := cacheStats(dbP)
		pressMismatches += mm
		sort.Slice(pressDurs, func(i, j int) bool { return pressDurs[i] < pressDurs[j] })
		pressP50v = append(pressP50v, stats.Pct(pressDurs, 500))
		pressP99v = append(pressP99v, stats.Pct(pressDurs, 990))
		pressP999v = append(pressP999v, stats.Pct(pressDurs, 999))
		pressHitsV = append(pressHitsV, h1-h0)
		pressMissV = append(pressMissV, m1-m0)
	}
	pressP50 := stats.Collect(pressP50v)
	pressP99 := stats.Collect(pressP99v)
	pressP999 := stats.Collect(pressP999v)
	pressHits := stats.Collect(pressHitsV)
	pressMiss := stats.Collect(pressMissV)
	check(dbP.Close())

	rep := report{
		Records:           records,
		RecordBytes:       recordBytes,
		Gets:              gets,
		BytesPerRecord:    bytesPerRecord,
		DictBytes:         dictBytes,
		DecodeAllocsPerOp: decodeAllocs,
		Scan: scanReport{
			Keys:        scanKeys,
			NsPerKey:    scanNsKey.Median,
			MBPerS:      scanMBsS.Median,
			P99Ns:       float64(scanP99.Median),
			Mismatches:  scanMismatches,
			NsPerKeyMin: scanNsKey.Min, NsPerKeyMax: scanNsKey.Max,
			MBPerSMin: scanMBsS.Min, MBPerSMax: scanMBsS.Max,
			P99Min: float64(scanP99.Min), P99Max: float64(scanP99.Max),
		},
		Ingest: ingestReport{
			Records:            records,
			LogicalBytes:       ingestLogical,
			SSTableBytes:       sstBytes,
			BuildMs:            float64(buildElapsed.Microseconds()) / 1000,
			IngestMs:           float64(ingestElapsed.Microseconds()) / 1000,
			TotalMs:            float64((buildElapsed + ingestElapsed).Microseconds()) / 1000,
			MBPerS:             float64(ingestLogical) / 1e6 / (buildElapsed + ingestElapsed).Seconds(),
			PostReadMismatches: ingestedMismatches,
		},
		Compaction: compactionReport{
			FlushMs:              float64(flushElapsed.Microseconds()) / 1000,
			CompactMs:            float64(compactElapsed.Microseconds()) / 1000,
			DBBytesBeforeFlush:   dbBytesBeforeFlush,
			DBBytesAfterFlush:    dbBytesAfterFlush,
			DBBytesAfterCompact:  dbBytesAfterCompact,
			DBBytesFinal:         dbBytesFinal,
			FlushSSTBytes:        flushSST,
			CompactSSTBytes:      compactSST,
			CompactBytesRead:     compactRead,
			SSTBytesWritten:      sstWrittenTotal,
			WALBytesWritten:      walWritten,
			LogicalBytes:         logicalBytes,
			CoreBytes:            coreBytes,
			WriteAmplification:   float64(sstWrittenTotal) / float64(logicalBytes),
			WriteAmpStored:       float64(sstWrittenTotal) / float64(coreBytes),
			PostSampleMismatches: postSampleMismatches,
			PostScanKeys:         postScanKeys,
			PostScanMismatches:   postScanMismatches,
		},
		PointReadWarm: pointReport{
			Gets:        gets,
			CacheBytes:  pebbleDefaultCacheBytes,
			P50Ns:       float64(warmP50.Median),
			P99Ns:       float64(warmP99.Median),
			P999Ns:      float64(warmP999.Median),
			CacheHits:   warmHits.Median,
			CacheMisses: warmMiss.Median,
			HitRate:     hitRate(warmHits.Median, warmMiss.Median),
			Mismatches:  warmMismatches,
			P50Min:      float64(warmP50.Min), P50Max: float64(warmP50.Max),
			P99Min: float64(warmP99.Min), P99Max: float64(warmP99.Max),
			P999Min: float64(warmP999.Min), P999Max: float64(warmP999.Max),
		},
		PointReadPressure: pointReport{
			Gets:        gets,
			CacheBytes:  pressureCacheBytes,
			P50Ns:       float64(pressP50.Median),
			P99Ns:       float64(pressP99.Median),
			P999Ns:      float64(pressP999.Median),
			CacheHits:   pressHits.Median,
			CacheMisses: pressMiss.Median,
			HitRate:     hitRate(pressHits.Median, pressMiss.Median),
			Mismatches:  pressMismatches,
			P50Min:      float64(pressP50.Min), P50Max: float64(pressP50.Max),
			P99Min: float64(pressP99.Min), P99Max: float64(pressP99.Max),
			P999Min: float64(pressP999.Min), P999Max: float64(pressP999.Max),
		},
		Runs:      runs,
		Statistic: "median-of-runs",
		Note: "bytes_per_record is the machine-9 codec payload only (excludes Pebble keys, " +
			"seqnums, checksums, filters, WAL and LSM overhead; Pebble also snappy-compresses blocks). " +
			"scan throughput is decoded logical MB/s. The two datasets interleave on even/odd keys so " +
			"the ingested SSTable overlaps the flushed dataset and the forced compaction rewrites bytes. " +
			"Automatic compactions are disabled so the forced db.Compact is the only compaction measured. " +
			"db_bytes_after_compact is measured immediately after db.Compact and may include not-yet-unlinked " +
			"obsolete inputs; db_bytes_final is measured after db.Close (obsolete inputs unlinked). " +
			"write_amplification = all SST bytes Pebble wrote (flush+compaction, cumulative) / logical " +
			"source bytes; write_amplification_stored divides by the machine-9 core bytes handed to Pebble. " +
			"warm reads use Pebble's default 8 MiB cache; pressure reads reopen the same DB with a 128 KiB " +
			"cache (working set ~2.5 MB). Latency is single-host and the OS page cache is warm in both " +
			"read phases. scan and point-read latencies are the median across EKB_RUNS runs (min/max retained); " +
			"ingest and compaction are one-time wall-clock operations, reported single-shot.",
	}

	// --- human table -> stderr ----------------------------------------------
	w := tabwriter.NewWriter(os.Stderr, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "pebble integration depth (machine-9 cores, 80/20 skew)")
	fmt.Fprintf(w, "records\t%d\n", records)
	fmt.Fprintf(w, "bytes_per_record (codec payload)\t%.4f\n", rep.BytesPerRecord)
	fmt.Fprintf(w, "scan keys\t%d\n", rep.Scan.Keys)
	fmt.Fprintf(w, "scan ns/key\t%.0f\n", rep.Scan.NsPerKey)
	fmt.Fprintf(w, "scan MB/s\t%.0f\n", rep.Scan.MBPerS)
	fmt.Fprintf(w, "scan p99\t%.0f ns\n", rep.Scan.P99Ns)
	fmt.Fprintf(w, "scan mismatches\t%d\n", rep.Scan.Mismatches)
	fmt.Fprintf(w, "ingest records\t%d\n", rep.Ingest.Records)
	fmt.Fprintf(w, "ingest build/ingest ms\t%.1f/%.1f\n", rep.Ingest.BuildMs, rep.Ingest.IngestMs)
	fmt.Fprintf(w, "ingest MB/s\t%.0f\n", rep.Ingest.MBPerS)
	fmt.Fprintf(w, "ingest sstable bytes\t%d\n", rep.Ingest.SSTableBytes)
	fmt.Fprintf(w, "ingest mismatches\t%d\n", rep.Ingest.PostReadMismatches)
	fmt.Fprintf(w, "flush ms\t%.1f\n", rep.Compaction.FlushMs)
	fmt.Fprintf(w, "compact ms\t%.1f\n", rep.Compaction.CompactMs)
	fmt.Fprintf(w, "db bytes before flush\t%d\n", rep.Compaction.DBBytesBeforeFlush)
	fmt.Fprintf(w, "db bytes after flush\t%d\n", rep.Compaction.DBBytesAfterFlush)
	fmt.Fprintf(w, "db bytes after compact\t%d\n", rep.Compaction.DBBytesAfterCompact)
	fmt.Fprintf(w, "db bytes final (post-close)\t%d\n", rep.Compaction.DBBytesFinal)
	fmt.Fprintf(w, "compact sst bytes written/read\t%d/%d\n", rep.Compaction.CompactSSTBytes, rep.Compaction.CompactBytesRead)
	fmt.Fprintf(w, "write amplification\t%.3f\t(stored %.3f)\n", rep.Compaction.WriteAmplification, rep.Compaction.WriteAmpStored)
	fmt.Fprintf(w, "post-compact sample mismatches\t%d\n", rep.Compaction.PostSampleMismatches)
	fmt.Fprintf(w, "post-compact scan keys\t%d\tmismatches\t%d\n", rep.Compaction.PostScanKeys, rep.Compaction.PostScanMismatches)
	fmt.Fprintf(w, "warm p50/p99/p99.9\t%.0f/%.0f/%.0f ns\thits\t%d\tmisses\t%d\n", rep.PointReadWarm.P50Ns, rep.PointReadWarm.P99Ns, rep.PointReadWarm.P999Ns, rep.PointReadWarm.CacheHits, rep.PointReadWarm.CacheMisses)
	fmt.Fprintf(w, "pressure p50/p99/p99.9\t%.0f/%.0f/%.0f ns\thits\t%d\tmisses\t%d\n", rep.PointReadPressure.P50Ns, rep.PointReadPressure.P99Ns, rep.PointReadPressure.P999Ns, rep.PointReadPressure.CacheHits, rep.PointReadPressure.CacheMisses)
	w.Flush()

	total := scanMismatches + ingestedMismatches + postSampleMismatches + postScanMismatches + warmMismatches + pressMismatches
	if total != 0 {
		fmt.Fprintf(os.Stderr, "FATAL: %d round-trip mismatches\n", total)
		os.Exit(1)
	}

	// prom -> evidence/pebble-integration.prom.
	if err := writeProm(rep); err != nil {
		fmt.Fprintln(os.Stderr, "write prom:", err)
	}

	// JSON -> stdout ONLY, with provenance stamped so the artifact is self-describing.
	out, err := json.MarshalIndent(rep, "", "  ")
	check(err)
	fmt.Print(string(provenance.Stamp(out)))
}

func hitRate(hits, misses int64) float64 {
	if hits+misses == 0 {
		return 0
	}
	return float64(hits) / float64(hits+misses)
}

// buildTrace builds the deterministic 80/20 access trace over n dataset-1 keys.
func buildTrace(n int) []int {
	tr := make([]int, gets)
	s := uint64(0x5EED1234)
	for i := range tr {
		tr[i] = workload.SkewIndex(&s, n)
	}
	return tr
}

// runGets fetches each traced dataset-1 key (even key 2*idx), decoding the core
// into the reused destination buffer and verifying it equals the original bytes.
func runGets(db *pebble.DB, dec *ekbloom.DictDecoder, trace []int, src [][]byte) (durs []time.Duration, mismatches int) {
	durs = make([]time.Duration, len(trace))
	var kb [8]byte
	for i, idx := range trace {
		binary.BigEndian.PutUint64(kb[:], uint64(2*idx))
		t0 := time.Now()
		core, closer, err := db.Get(kb[:])
		if err != nil {
			check(err)
		}
		n, derr := dec.DecodeInto(decodeDst, core)
		closer.Close()
		durs[i] = time.Since(t0)
		if derr != nil {
			check(derr)
		}
		if string(decodeDst[:n]) != string(src[idx]) {
			mismatches++
		}
	}
	return durs, mismatches
}

// scanRange iterates [lo, hi), decoding and verifying every value. It returns
// the number of keys seen, the wall time, the per-key durations (for a p99) and
// the mismatch count.
func scanRange(db *pebble.DB, dec *ekbloom.DictDecoder, lo, hi int, srcFor func(uint64) ([]byte, bool)) (keys int, elapsed time.Duration, durs []time.Duration, mismatches int) {
	var lb, ub [8]byte
	binary.BigEndian.PutUint64(lb[:], uint64(lo))
	binary.BigEndian.PutUint64(ub[:], uint64(hi))
	durs = make([]time.Duration, 0, hi-lo)
	it, err := db.NewIter(&pebble.IterOptions{LowerBound: lb[:], UpperBound: ub[:]})
	check(err)
	defer it.Close()
	t0 := time.Now()
	for it.First(); it.Valid(); it.Next() {
		idx := binary.BigEndian.Uint64(it.Key())
		v := it.Value()
		s0 := time.Now()
		n, derr := dec.DecodeInto(decodeDst, v)
		durs = append(durs, time.Since(s0))
		if derr != nil {
			check(derr)
		}
		keys++
		if want, ok := srcFor(idx); !ok || string(decodeDst[:n]) != string(want) {
			mismatches++
		}
	}
	elapsed = time.Since(t0)
	check(it.Error())
	return keys, elapsed, durs, mismatches
}

// verifyRange point-reads every key in [lo, hi) and verifies it.
func verifyRange(db *pebble.DB, dec *ekbloom.DictDecoder, lo, hi uint64, srcFor func(uint64) ([]byte, bool)) (checked, mismatches int, durs []time.Duration) {
	durs = make([]time.Duration, 0, int(hi-lo))
	var kb [8]byte
	for k := lo; k < hi; k++ {
		binary.BigEndian.PutUint64(kb[:], k)
		t0 := time.Now()
		core, closer, err := db.Get(kb[:])
		if err != nil {
			check(err)
		}
		n, derr := dec.DecodeInto(decodeDst, core)
		closer.Close()
		durs = append(durs, time.Since(t0))
		if derr != nil {
			check(derr)
		}
		checked++
		if want, ok := srcFor(k); !ok || string(decodeDst[:n]) != string(want) {
			mismatches++
		}
	}
	return checked, mismatches, durs
}

// sampleVerify point-reads a spread sample of keys from both datasets.
func sampleVerify(db *pebble.DB, dec *ekbloom.DictDecoder, srcFor func(uint64) ([]byte, bool)) (mismatches int) {
	var kb [8]byte
	checkKey := func(k uint64) {
		binary.BigEndian.PutUint64(kb[:], k)
		core, closer, err := db.Get(kb[:])
		if err != nil {
			check(err)
		}
		n, derr := dec.DecodeInto(decodeDst, core)
		closer.Close()
		if derr != nil {
			check(derr)
		}
		if want, ok := srcFor(k); !ok || string(decodeDst[:n]) != string(want) {
			mismatches++
		}
	}
	for i := 0; i < records; i += 10 {
		checkKey(uint64(2 * i))   // dataset 1
		checkKey(uint64(2*i + 1)) // dataset 2
	}
	return mismatches
}

// writeSSTable builds an offline SSTable whose values are machine-9 cores on odd
// keys, in ascending order, and syncs it to disk for ingestion. It returns the
// total core bytes written.
func writeSSTable(path string, src [][]byte, dict ekbloom.Dictionary) uint64 {
	f, err := vfs.Default.Create(path)
	check(err)
	w := sstable.NewWriter(objstorageprovider.NewFileWritable(f), sstable.WriterOptions{})
	var kb [8]byte
	var coreBytes uint64
	for i, v := range src {
		core, err := dict.EncodeRecord(v)
		check(err)
		coreBytes += uint64(len(core))
		binary.BigEndian.PutUint64(kb[:], uint64(2*i+1))
		check(w.Set(kb[:], core))
	}
	// Writer.Close flushes, Sync()s and closes the underlying writable.
	check(w.Close())
	return coreBytes
}

// cacheStats returns the cumulative block-cache hits and misses.
func cacheStats(db *pebble.DB) (hits, misses int64) {
	m := db.Metrics()
	return m.BlockCache.Hits, m.BlockCache.Misses
}

// sstWritten sums the cumulative bytes written by flushes and compactions.
func sstWritten(m *pebble.Metrics) (flushed, compacted uint64) {
	for _, l := range m.Levels {
		flushed += l.BytesFlushed
		compacted += l.BytesCompacted
	}
	return flushed, compacted
}

// bytesRead sums the cumulative bytes read for compactions.
func bytesRead(m *pebble.Metrics) uint64 {
	var read uint64
	for _, l := range m.Levels {
		read += l.BytesRead
	}
	return read
}

// dirSize sums the sizes of all regular files under dir.
func dirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// writeProm writes the Prometheus-style exposition to evidence/.
func writeProm(rep report) error {
	if err := os.MkdirAll("../evidence", 0o755); err != nil {
		return err
	}
	var b []byte
	b = append(b, "# EKBLOOM machine-9 Pebble integration-depth study\n"...)
	b = append(b, "# generated by courts/pebblebench (go run ./pebblebench)\n"...)
	line := func(format string, args ...any) {
		b = append(b, fmt.Sprintf(format, args...)...)
		b = append(b, '\n')
	}
	g := func(name string, v float64) {
		line("# TYPE ekbloom_pebble_%s gauge", name)
		line("ekbloom_pebble_%s %g", name, v)
	}
	g("bytes_per_record", rep.BytesPerRecord)
	g("dict_bytes", float64(rep.DictBytes))
	g("decode_allocs_per_op", rep.DecodeAllocsPerOp)
	g("scan_keys", float64(rep.Scan.Keys))
	g("scan_ns_per_key", rep.Scan.NsPerKey)
	g("scan_mb_per_s", rep.Scan.MBPerS)
	g("scan_p99_ns", rep.Scan.P99Ns)
	g("scan_mismatches", float64(rep.Scan.Mismatches))
	g("ingest_ms", rep.Ingest.IngestMs)
	g("ingest_total_ms", rep.Ingest.TotalMs)
	g("ingest_mb_per_s", rep.Ingest.MBPerS)
	g("ingest_sstable_bytes", float64(rep.Ingest.SSTableBytes))
	g("ingest_mismatches", float64(rep.Ingest.PostReadMismatches))
	g("flush_ms", rep.Compaction.FlushMs)
	g("compaction_ms", rep.Compaction.CompactMs)
	g("db_bytes_before_flush", float64(rep.Compaction.DBBytesBeforeFlush))
	g("db_bytes_after_compact", float64(rep.Compaction.DBBytesAfterCompact))
	g("db_bytes_final", float64(rep.Compaction.DBBytesFinal))
	g("compact_sst_bytes_written", float64(rep.Compaction.CompactSSTBytes))
	g("compact_bytes_read", float64(rep.Compaction.CompactBytesRead))
	g("write_amplification", rep.Compaction.WriteAmplification)
	g("write_amplification_stored", rep.Compaction.WriteAmpStored)
	g("post_sample_mismatches", float64(rep.Compaction.PostSampleMismatches))
	g("post_scan_mismatches", float64(rep.Compaction.PostScanMismatches))
	g("point_read_warm_p50_ns", rep.PointReadWarm.P50Ns)
	g("point_read_warm_p99_ns", rep.PointReadWarm.P99Ns)
	g("point_read_warm_p99_9_ns", rep.PointReadWarm.P999Ns)
	g("point_read_warm_cache_hits", float64(rep.PointReadWarm.CacheHits))
	g("point_read_warm_cache_misses", float64(rep.PointReadWarm.CacheMisses))
	g("point_read_pressure_p50_ns", rep.PointReadPressure.P50Ns)
	g("point_read_pressure_p99_ns", rep.PointReadPressure.P99Ns)
	g("point_read_pressure_p99_9_ns", rep.PointReadPressure.P999Ns)
	g("point_read_pressure_cache_hits", float64(rep.PointReadPressure.CacheHits))
	g("point_read_pressure_cache_misses", float64(rep.PointReadPressure.CacheMisses))
	g("point_read_pressure_cache_bytes", float64(rep.PointReadPressure.CacheBytes))
	g("roundtrip_mismatches", float64(rep.Scan.Mismatches+rep.Ingest.PostReadMismatches+rep.Compaction.PostSampleMismatches+rep.Compaction.PostScanMismatches+rep.PointReadWarm.Mismatches+rep.PointReadPressure.Mismatches))
	return os.WriteFile("../evidence/pebble-integration.prom", b, 0o644)
}
