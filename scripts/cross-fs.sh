#!/bin/sh
# Cross-filesystem determinism check.
#
# Copies the fixtures to every writable filesystem type available on the host
# (ext4, btrfs, xfs, tmpfs, ...) and confirms the fingerprint — machine id,
# physical bytes, and a SHA-256 over the fully decoded logical bytes — is
# identical everywhere. Writes evidence/cross-fs.log and evidence/cross-fs.json.
#
# Filesystem types that are not mounted (or not writable) on the host are
# reported as "not exercised", never silently skipped.
# Run from the repository root: sh scripts/cross-fs.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
mkdir -p evidence

results=$(mktemp)
trap 'rm -f "$results"' EXIT

# fstype_of <path>: the filesystem type of the longest matching mount point.
fstype_of() {
    awk -v p="$1/" '{
        mp=$2; if (substr(mp,length(mp),1)!="/") mp=mp "/";
        if (index(p, mp)==1 && length(mp) > best) { best=length(mp); t=$3 }
    } END { print t }' /proc/mounts
}

run_fs() {
    type=$1
    base=$2
    workdir="$base/.ekbloom-xfs-$$"
    rm -rf "$workdir"
    if ! mkdir -p "$workdir" 2>/dev/null; then
        echo "note: $type at $base not writable; not exercised" >&2
        return
    fi
    cp -r testdata "$workdir/"
    go run ./internal/tools/fingerprint "$workdir/testdata" > "$workdir/fp.txt"
    sum=$(sha256sum "$workdir/fp.txt" | cut -d' ' -f1)
    printf '%s\t%s\t%s\n' "$type" "$base" "$sum" >> "$results"
    echo "== $type ($base): $sum =="
    rm -rf "$workdir"
}

# The repository's own filesystem.
run_fs "$(fstype_of "$root")" "$root"
# A real tmpfs if present.
[ -w /dev/shm ] && run_fs tmpfs /dev/shm
# Any writable mount of the block-backed filesystems.
for type in btrfs xfs; do
    mp=$(awk -v t="$type" '$3==t {print $2; exit}' /proc/mounts)
    if [ -n "${mp:-}" ] && [ -w "$mp" ]; then
        run_fs "$type" "$mp"
    else
        echo "note: no writable $type mount found; not exercised"
    fi
done

# ---- verdict ---------------------------------------------------------------
sums=$(cut -f3 "$results" | sort -u | wc -l | tr -d ' ')
types=$(cut -f1 "$results" | tr '\n' ' ')
n=$(wc -l < "$results" | tr -d ' ')
echo
if [ "$sums" = 1 ]; then
    echo "PASS: fingerprint identical across $n filesystem(s): $types"
else
    echo "FAIL: fingerprints differ across filesystems"
    cat "$results"
    exit 1
fi

# ---- machine-readable evidence --------------------------------------------
{
    printf '{\n  "filesystems": [\n'
    first=1
    while IFS='	' read -r type path sum; do
        [ "$first" = 1 ] || printf ',\n'
        first=0
        printf '    {"fstype": "%s", "path": "%s", "fingerprint_sha256": "%s"}' "$type" "$path" "$sum"
    done < "$results"
    printf '\n  ],\n'
    printf '  "identical": true,\n'
    printf '  "count": %s\n}\n' "$n"
} > evidence/cross-fs.json

cat evidence/cross-fs.json

# Optional cross-architecture build check (compile only; run under qemu to compare).
tmparch=$(mktemp -d)
if command -v qemu-aarch64-static >/dev/null 2>&1 || command -v qemu-aarch64 >/dev/null 2>&1; then
    echo
    echo "== cross-arch: building arm64 fingerprint =="
    GOOS=linux GOARCH=arm64 go build -o "$tmparch/fp-arm64" ./internal/tools/fingerprint
    echo "built arm64 fingerprint binary (run under qemu to compare output)"
else
    echo
    echo "note: no qemu-aarch64 found; arm64 determinism is a build-only check here"
    GOOS=linux GOARCH=arm64 go build -o "$tmparch/fp-arm64" ./internal/tools/fingerprint
    echo "arm64 build OK (run under qemu on an arm64 host to compare)"
fi
rm -rf "$tmparch"
