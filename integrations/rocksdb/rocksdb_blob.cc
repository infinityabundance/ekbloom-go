// rocksdb_blob.cc — value-level integration of the EKBLOOM machine-9
// shared-dictionary codec into RocksDB, plus a registry-loaded MergeOperator.
//
// Why value-level and not a `CompressionType`?
// RocksDB's compression is a compile-time enum (kSnappyCompression, ...); there
// is no runtime registration hook for a new CompressionType — the plugin system
// (ObjectLibrary/ObjectRegistry) instantiates *customizable classes*
// (MergeOperator, SstPartitioner, ...) by name, not compressors. So the honest
// integration is the value: Put stores ekbloom_compress(value), Get returns
// ekbloom_decompress(value), and the shared dictionary is stored once in the DB
// alongside the values. See README.md.
//
// The MergeOperator is registered as a plugin in a ConfigOptions object registry
// (ConfigOptions::registry->AddLibrary(...)) and then instantiated *through* that
// registry (ObjectRegistry::NewSharedObject<MergeOperator>), which is the
// supported runtime-plugin path. It implements read-modify-write over the
// encoded values: decode existing + decode each operand -> concat raw -> re-encode.
//
// Build: see Dockerfile.rocksdb. Output: a single JSON object on stdout.

#include <rocksdb/convenience.h>
#include <rocksdb/db.h>
#include <rocksdb/merge_operator.h>
#include <rocksdb/options.h>
#include <rocksdb/utilities/object_registry.h>

#include "libekbloom.h"

#include <algorithm>
#include <chrono>
#include <cstdint>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <filesystem>
#include <memory>
#include <string>
#include <vector>

namespace {

constexpr int kRecords = 20000;      // records loaded into the DB
constexpr int kRecordBytes = 100;    // record size (logical bytes)
constexpr int kOps = 50000;          // timed operations
constexpr int kMergeKeys = 64;       // append keyspace for the merge workload
constexpr int kHotKeys = 64;         // merge-operand vocabulary (repetitive)
constexpr char kMergeName[] = "EkbloomBlobMerge";

// Deterministic, repetitive record generator (a small token alphabet), so a
// shared order-0 dictionary is a fair fit for the corpus.
const char* const kTokens[] = {
    "id=",      "user",      "kind=",   "alpha",     "beta",    "gamma",
    "region=",  "us-east-1", "eu-west-1", "ap-south-1", "score=", "000",
    "111",      "222",       "status=", "active",    "idle",    "|"};
constexpr int kNumTokens = static_cast<int>(sizeof(kTokens) / sizeof(kTokens[0]));

void GenRecord(uint64_t seed, std::string* out) {
  out->clear();
  uint64_t k = seed;
  while (out->size() < static_cast<size_t>(kRecordBytes)) {
    k = k * 6364136223846793005ULL + 1442695040888963407ULL;
    out->append(kTokens[(k >> 33) % kNumTokens]);
  }
  out->resize(kRecordBytes);
}

std::string KeyOf(int i) {
  char b[32];
  std::snprintf(b, sizeof(b), "r:%08d", i);
  return std::string(b);
}

// splitmix64: a small, deterministic PRNG so the trace is reproducible.
uint64_t NextRand(uint64_t* s) {
  uint64_t z = (*s += 0x9E3779B97F4A7C15ULL);
  z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9ULL;
  z = (z ^ (z >> 27)) * 0x94D049BB133111EBULL;
  return z ^ (z >> 31);
}

// 80/20 key skew: 80% of picks hit the hottest 20% of keys, 20% hit the rest.
int SkewedKey(uint64_t r, int n) {
  int hot = n / 5;
  if (hot < 1) hot = 1;
  int cold = n - hot;
  if (cold < 1) cold = 1;
  if (r % 100 < 80) return static_cast<int>((r >> 7) % static_cast<uint64_t>(hot));
  return hot + static_cast<int>((r >> 7) % static_cast<uint64_t>(cold));
}

int64_t NowNs() {
  return std::chrono::duration_cast<std::chrono::nanoseconds>(
             std::chrono::steady_clock::now().time_since_epoch())
      .count();
}

// Runs reports the number of measurement runs R, from the same environment knob
// the Go harnesses use (internal/tools/stat.Runs): EKB_RUNS if set to a positive
// integer, else 3 when CI is set (CI must stay time-bounded), else 5.
int Runs() {
  int def = 5;
  if (std::getenv("CI") != nullptr) def = 3;
  if (const char* v = std::getenv("EKB_RUNS")) {
    char* end = nullptr;
    const long n = std::strtol(v, &end, 10);
    if (end != v && n > 0) return static_cast<int>(n);
  }
  return def;
}

// Median sorts a copy of v and returns its middle element (no interpolation), so
// the reported value is always one observed run.
template <typename T>
T Median(std::vector<T> v) {
  std::sort(v.begin(), v.end());
  return v[v.size() / 2];
}

// The cgo-generated C ABI declares the input pointers non-const (C has no
// const-qualified function signatures here); the bridge only reads them.
unsigned char* Mut(const char* p) {
  return reinterpret_cast<unsigned char*>(const_cast<char*>(p));
}

// EncodeValue compresses raw with the shared dictionary into *out. The rANS
// encoder emits a small header plus at most ~2 bytes per input symbol, so a
// generous cap avoids a resize-and-retry.
bool EncodeValue(const std::string& raw, std::string* out) {
  const int cap = static_cast<int>(raw.size()) * 2 + 256;
  out->resize(static_cast<size_t>(cap));
  int n = ekbloom_compress(Mut(raw.data()), static_cast<int>(raw.size()),
                           reinterpret_cast<unsigned char*>(&(*out)[0]), cap);
  if (n < 0) return false;
  out->resize(static_cast<size_t>(n));
  return true;
}

// DecodeValue decompresses an encoded value. The 4-byte little-endian logical
// length sits at the front of a machine-9 core, so the output cap is known.
bool DecodeValue(const char* data, size_t size, std::string* out) {
  if (size < 4) return false;
  uint32_t logical = 0;
  std::memcpy(&logical, data, 4);
  if (logical == 0) {
    out->clear();
    return true;
  }
  out->resize(logical);
  int n = ekbloom_decompress(Mut(data), static_cast<int>(size),
                             reinterpret_cast<unsigned char*>(&(*out)[0]),
                             static_cast<int>(logical));
  if (n < 0 || static_cast<uint32_t>(n) != logical) return false;
  out->resize(static_cast<size_t>(n));
  return true;
}

bool DecodeValue(const std::string& enc, std::string* out) {
  return DecodeValue(enc.data(), enc.size(), out);
}

// DecodeAppend decodes an encoded value and appends the raw bytes to *out.
bool DecodeAppend(const char* data, size_t size, std::string* out) {
  std::string tmp;
  if (!DecodeValue(data, size, &tmp)) return false;
  out->append(tmp);
  return true;
}

// EkbloomBlobMergeOperator: decode + concat + re-encode.
class EkbloomBlobMergeOperator : public rocksdb::MergeOperator {
 public:
  const char* Name() const override { return "EkbloomBlobMergeOperator"; }

  bool FullMergeV2(const MergeOperationInput& merge_in,
                   MergeOperationOutput* merge_out) const override {
    std::string raw;
    if (merge_in.existing_value != nullptr &&
        !DecodeValue(merge_in.existing_value->data(),
                     merge_in.existing_value->size(), &raw)) {
      return false;
    }
    for (const rocksdb::Slice& operand : merge_in.operand_list) {
      if (!DecodeAppend(operand.data(), operand.size(), &raw)) {
        return false;
      }
    }
    std::string encoded;
    if (!EncodeValue(raw, &encoded)) {
      return false;
    }
    merge_out->new_value.assign(encoded);
    return true;
  }
};

// The factory registered in the library. It hands ownership to the caller's
// guard (required for ObjectRegistry::NewSharedObject).
rocksdb::MergeOperator* NewEkbloomMerge(
    const std::string& /*name*/,
    std::unique_ptr<rocksdb::MergeOperator>* guard, std::string* /*err*/) {
  guard->reset(new EkbloomBlobMergeOperator());
  return guard->get();
}

}  // namespace

int main() {
  char tmpl[] = "/tmp/ekbloom-rocksdb-XXXXXX";
  char* made = mkdtemp(tmpl);
  if (made == nullptr) {
    std::fprintf(stderr, "mkdtemp failed\n");
    return 1;
  }
  const std::string dir = made;

  // 1. Register the MergeOperator as a runtime plugin and load it *through* the
  //    registry. This is the supported extensibility path in RocksDB 8.x.
  rocksdb::ConfigOptions config_options;
  config_options.registry = rocksdb::ObjectRegistry::NewInstance();
  auto library = std::make_shared<rocksdb::ObjectLibrary>("ekbloom");
  library->AddFactory<rocksdb::MergeOperator>(kMergeName, &NewEkbloomMerge);
  config_options.registry->AddLibrary(library);

  std::shared_ptr<rocksdb::MergeOperator> merge_operator;
  rocksdb::Status ms = config_options.registry->NewSharedObject<rocksdb::MergeOperator>(
      kMergeName, &merge_operator);
  if (!ms.ok()) {
    std::fprintf(stderr, "could not load MergeOperator from registry: %s\n",
                 ms.ToString().c_str());
    return 1;
  }

  rocksdb::Options options;
  options.create_if_missing = true;
  options.compression = rocksdb::kNoCompression;  // the values are already encoded
  options.merge_operator = merge_operator;

  rocksdb::DB* db = nullptr;
  rocksdb::Status s = rocksdb::DB::Open(options, dir, &db);
  if (!s.ok()) {
    std::fprintf(stderr, "open failed: %s\n", s.ToString().c_str());
    return 1;
  }

  rocksdb::WriteOptions wo;
  wo.disableWAL = true;
  rocksdb::ReadOptions ro;

  // 2. Build the corpus (length-prefixed frames) and train the one dictionary.
  std::vector<std::string> records(kRecords);
  std::string corpus;
  corpus.reserve(static_cast<size_t>(kRecords) * (4 + kRecordBytes));
  for (int i = 0; i < kRecords; ++i) {
    GenRecord(static_cast<uint64_t>(i), &records[i]);
    uint32_t len = static_cast<uint32_t>(records[i].size());
    corpus.append(reinterpret_cast<const char*>(&len), 4);
    corpus.append(records[i]);
  }
  unsigned char dict_id[16] = {0};
  int tr = ekbloom_train(Mut(corpus.data()), static_cast<int>(corpus.size()),
                         dict_id);
  if (tr != 0) {
    std::fprintf(stderr, "ekbloom_train failed: %d\n", tr);
    return 1;
  }
  const int dict_bytes = ekbloom_dict_bytes();
  std::fprintf(stderr, "trained dictionary: %d bytes, id=", dict_bytes);
  for (int i = 0; i < 16; ++i) std::fprintf(stderr, "%02x", dict_id[i]);
  std::fprintf(stderr, "\n");

  // Store the dictionary once, uncompressed, under a reserved key so the values
  // remain decodable after a restart.
  std::string dict_blob(static_cast<size_t>(dict_bytes), '\0');
  int dn = ekbloom_dict(reinterpret_cast<unsigned char*>(&dict_blob[0]), dict_bytes);
  if (dn != dict_bytes) {
    std::fprintf(stderr, "ekbloom_dict failed: %d\n", dn);
    return 1;
  }
  db->Put(wo, rocksdb::Slice("\x00:dict", 6), dict_blob);

  // 3. Bulk load: Put = compress.
  uint64_t physical = static_cast<uint64_t>(dict_bytes);
  std::string enc;
  for (int i = 0; i < kRecords; ++i) {
    if (!EncodeValue(records[i], &enc)) {
      std::fprintf(stderr, "encode failed at %d\n", i);
      return 1;
    }
    physical += enc.size();
    if (rocksdb::Status ps = db->Put(wo, KeyOf(i), enc); !ps.ok()) {
      std::fprintf(stderr, "put failed at %d: %s\n", i, ps.ToString().c_str());
      return 1;
    }
  }
  db->Flush(rocksdb::FlushOptions());

  // Every stored record must decode byte-exact (Get = decompress).
  int decode_mismatch = 0;
  for (int i = 0; i < kRecords; ++i) {
    std::string v, raw;
    db->Get(ro, KeyOf(i), &v);
    if (!DecodeValue(v, &raw) || raw != records[i]) ++decode_mismatch;
  }
  if (decode_mismatch != 0) {
    std::fprintf(stderr, "decode mismatches: %d\n", decode_mismatch);
    return 1;
  }

  // 4. Prove the MergeOperator: base + N operands -> decode == concatenation.
  {
    std::string base = "hello-";
    std::string expected = base;
    std::string encoded;
    if (!EncodeValue(base, &encoded)) return 1;
    db->Put(wo, "m:test", encoded);
    for (int i = 0; i < 16; ++i) {
      std::string op = "x" + std::to_string(i);
      expected += op;
      std::string enc_op;
      if (!EncodeValue(op, &enc_op)) return 1;
      db->Merge(wo, "m:test", enc_op);
    }
    std::string v, raw;
    rocksdb::Status gs = db->Get(ro, "m:test", &v);
    if (!gs.ok() || !DecodeValue(v, &raw) || raw != expected) {
      std::fprintf(stderr, "merge operator round-trip failed (get=%s, %zu vs %zu bytes)\n",
                   gs.ToString().c_str(), raw.size(), expected.size());
      return 1;
    }
  }

  // 5. Timed workload: repeat the 50,000-op 80/20 read:merge workload R times
  //    (R = EKB_RUNS, default 5; 3 in CI) and report the MEDIAN get p50/p99 across
  //    runs (median-of-runs), with the observed min/max retained. Each run replays
  //    the identical deterministic trace (the RNG is reset per run) purely to
  //    expose run-to-run spread; the timed reads are all on the record keyspace,
  //    so the append-only merge keyspace does not affect the measured get latency.
  const int runs = Runs();
  std::vector<int64_t> p50s, p99s;
  p50s.reserve(static_cast<size_t>(runs));
  p99s.reserve(static_cast<size_t>(runs));
  size_t ng = 0;
  for (int run = 0; run < runs; ++run) {
    std::vector<int64_t> gets;
    gets.reserve(kOps);
    uint64_t rng = 0x123456789ABCDEFULL;
    int read_mismatch = 0;
    for (int i = 0; i < kOps; ++i) {
      uint64_t r = NextRand(&rng);
      bool is_read = (r % 100) < 80;
      uint64_t r2 = NextRand(&rng);
      if (is_read) {
        int ki = SkewedKey(r2, kRecords);
        const int64_t t0 = NowNs();
        std::string v, raw;
        db->Get(ro, KeyOf(ki), &v);
        bool ok = DecodeValue(v, &raw);
        const int64_t t1 = NowNs();
        gets.push_back(t1 - t0);
        if (!ok || raw != records[ki]) ++read_mismatch;
      } else {
        int mk = static_cast<int>(r2 % kMergeKeys);
        std::string op = "op" + std::to_string(r2 % kHotKeys) + ":";
        std::string enc_op;
        if (!EncodeValue(op, &enc_op)) return 1;
        db->Merge(wo, "m:op:" + std::to_string(mk), enc_op);
      }
    }
    if (read_mismatch != 0) {
      std::fprintf(stderr, "read mismatches: %d\n", read_mismatch);
      return 1;
    }
    std::sort(gets.begin(), gets.end());
    ng = gets.size();
    p50s.push_back(ng ? gets[ng * 50 / 100] : 0);
    p99s.push_back(ng ? gets[std::min(ng - 1, ng * 99 / 100)] : 0);
  }

  const int64_t p50 = Median(p50s);
  const int64_t p99 = Median(p99s);
  const int64_t p50_min = *std::min_element(p50s.begin(), p50s.end());
  const int64_t p50_max = *std::max_element(p50s.begin(), p50s.end());
  const int64_t p99_min = *std::min_element(p99s.begin(), p99s.end());
  const int64_t p99_max = *std::max_element(p99s.begin(), p99s.end());
  const long breaches = ekbloom_breaches();

  db->Close();
  delete db;
  std::error_code ec;
  std::filesystem::remove_all(dir, ec);

  const double bytes_per_record = static_cast<double>(physical) / kRecords;
  const double expansion =
      static_cast<double>(physical) / (static_cast<double>(kRecords) * kRecordBytes);

  std::fprintf(stderr,
               "records=%d dict_bytes=%d physical_bytes=%llu runs=%d get_n=%zu "
               "merge_ops=%d breaches=%ld\n",
               kRecords, dict_bytes, static_cast<unsigned long long>(physical), runs,
               ng, kOps - static_cast<int>(ng), breaches);

  std::printf(
      "{\"records\":%d,\"record_bytes\":%d,\"ops\":%d,\"runs\":%d,"
      "\"statistic\":\"median-of-runs\",\"bytes_per_record\":%.6f,"
      "\"get_p50_ns\":%lld,\"get_p99_ns\":%lld,"
      "\"get_p50_ns_min\":%lld,\"get_p50_ns_max\":%lld,"
      "\"get_p99_ns_min\":%lld,\"get_p99_ns_max\":%lld,"
      "\"ekbloom_physical_bytes\":%llu,"
      "\"ekbloom_expansion_ratio\":%.6f,\"breaches\":%ld}\n",
      kRecords, kRecordBytes, kOps, runs, bytes_per_record,
      static_cast<long long>(p50), static_cast<long long>(p99),
      static_cast<long long>(p50_min), static_cast<long long>(p50_max),
      static_cast<long long>(p99_min), static_cast<long long>(p99_max),
      static_cast<unsigned long long>(physical), expansion, breaches);
  return 0;
}
