# EKBLOOM × RocksDB — value-codec integration

This directory integrates the EKBLOOM **machine-9 shared-dictionary codec** into
RocksDB at the **value level**, and adds a **registered `MergeOperator`** that
performs read-modify-write over the encoded values.

## The honest boundary: RocksDB has no runtime compression plugin

RocksDB compression is a **compile-time enum** (`rocksdb::CompressionType`:
`kSnappyCompression`, `kZSTD`, …) resolved by the `CompressionType` switch in the
library binary. There is **no registry hook to add a new `CompressionType` at
runtime**: the plugin system (`ObjectLibrary` / `ObjectRegistry`, reached through
`ConfigOptions::registry`) instantiates *customizable classes* such as
`MergeOperator`, `SstPartitioner`, `TablePropertiesCollectorFactory` and a handful
of others — not compressors. Any "EKBLOOM compression for RocksDB" that claims to
plug into the block compressor would have to **fork/relink RocksDB**, which is not
what this is.

So the integration is deliberately **value-level**:

| Layer | Mechanism |
| --- | --- |
| Values | `Put` stores `ekbloom_compress(value)`; `Get` returns `ekbloom_decompress(value)`. |
| Model | One trained `Dictionary` (540 B) is stored once under a reserved key, so values survive a restart. |
| Read-modify-write | A `rocksdb::MergeOperator` is registered as a plugin and loaded **through** `ConfigOptions.registry`, implementing decode + concat + re-encode. |

The shared dictionary reflects the corpus distribution, so repetitive small
records (the value-log / BlobDB sweet spot) shrink substantially; incompressible
or already-compressed values are a bad fit, exactly as with any static dictionary.

## Layout

| Path | What |
| --- | --- |
| `bridge/` | **Separate Go module** (`replace github.com/infinityabundance/ekbloom-go => ../../../`) that builds the `c-shared` library `libekbloom.so` (+ `libekbloom.h`). |
| `bridge/bridge.go` | The whole C ABI: `ekbloom_train`, `ekbloom_compress`, `ekbloom_decompress` (+ `ekbloom_dict`, `ekbloom_dict_bytes`, `ekbloom_breaches`), around **one mutex-guarded `Dictionary`**. |
| `rocksdb_blob.cc` | The RocksDB program: temp-dir DB, `Put`=compress / `Get`=decompress, the registry-registered `MergeOperator`, the workload, and the JSON report. |
| `Dockerfile.rocksdb` | Multi-stage: `golang:1.23-bookworm` builds the `.so`; `ubuntu:24.04` (+ `librocksdb-dev build-essential`) compiles and runs the C++. |
| `../../scripts/rocksdb.sh` | Builds + runs capped; stdout → `evidence/rocksdb-blobdb.json`, log → `evidence/rocksdb-blobdb.log`. |

## C ABI (`bridge/bridge.go`)

```c
int  ekbloom_train(const unsigned char *corpus, int n, unsigned char *idOut);
int  ekbloom_compress(const unsigned char *src, int n, unsigned char *dst, int cap);
int  ekbloom_decompress(const unsigned char *src, int n, unsigned char *dst, int cap);
int  ekbloom_dict(unsigned char *dst, int cap);
int  ekbloom_dict_bytes(void);
long ekbloom_breaches(void);
```

- `ekbloom_train`: `corpus` is `n` bytes of consecutive **length-prefixed** records
  (`uint32 little-endian length || bytes`); returns `0` on success and writes the
  16-byte dictionary content id to `idOut`.
- `ekbloom_compress` / `ekbloom_decompress`: return the byte count written, or a
  negative code. The dictionary is shared and read-only after training, so these
  are safe to call concurrently.

## The MergeOperator

Registered in an `ObjectLibrary` and added to a `ConfigOptions` registry:

```cpp
rocksdb::ConfigOptions config_options;
config_options.registry = rocksdb::ObjectRegistry::NewInstance();
auto library = std::make_shared<rocksdb::ObjectLibrary>("ekbloom");
library->AddFactory<rocksdb::MergeOperator>("EkbloomBlobMerge", &NewEkbloomMerge);
config_options.registry->AddLibrary(library);

std::shared_ptr<rocksdb::MergeOperator> op;
config_options.registry->NewSharedObject<rocksdb::MergeOperator>("EkbloomBlobMerge", &op);
options.merge_operator = op;
```

`FullMergeV2` decodes the existing value and every operand, concatenates the raw
bytes, and re-encodes the result — so `Merge` is a codec-native append.

## The benchmark

- **20,000 records × 100 B**, generated deterministically from a small repetitive
  token alphabet so a shared order-0 dictionary is a fair fit.
- One dictionary trained on the corpus; every stored record asserted byte-exact on
  decode before timing.
- **50,000 timed ops**, 80/20 read:merge, reads drawn with an **80/20 key skew**
  (80% of reads hit the hottest 20% of keys). The merge branch exercises the
  registered operator.
- JSON on stdout:

```json
{"records":20000,"record_bytes":100,"ops":50000,"bytes_per_record":…,
 "get_p50_ns":…,"get_p99_ns":…,"ekbloom_physical_bytes":…,
 "ekbloom_expansion_ratio":…,"breaches":…}
```

Field definitions:

- `ekbloom_physical_bytes` — the dictionary (540 B, stored once) plus the sum of
  the encoded value lengths of the 20,000 record keys.
- `bytes_per_record` — `ekbloom_physical_bytes / records` (dictionary amortised).
- `ekbloom_expansion_ratio` — `ekbloom_physical_bytes / (records × record_bytes)`;
  `< 1` means the encoded set is smaller than the raw records (the reference's
  `physical / visible` shape).
- `breaches` — `ekbloom.ResourcePolicySnapshot().AllocationBreaches` (whole-object
  allocations refused by the 1 GiB default cap), read through the bridge.

## Run

```sh
sh scripts/rocksdb.sh
```

Requires Docker only; nothing is built on the host.
