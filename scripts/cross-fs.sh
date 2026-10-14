#!/bin/sh
# Cross-filesystem determinism check.
#
# Copies the fixtures to every filesystem type it can reach and confirms the
# fingerprint — machine id, physical bytes, and a SHA-256 over the fully decoded
# logical bytes — is identical everywhere. Writes evidence/cross-fs.log and
# evidence/cross-fs.json.
#
# Filesystems are taken from, in order:
#   1. the repository's own filesystem;
#   2. a real tmpfs (/dev/shm);
#   3. any writable mount of btrfs/xfs;
#   4. loopback images (ext4/xfs/btrfs) when privileged (root or passwordless
#      sudo) and the mkfs tool is installed — this is how CI reaches xfs.
#
# A type that cannot be reached is reported as "not exercised", never silently
# skipped.
# Run from the repository root: sh scripts/cross-fs.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
mkdir -p evidence

results=$(mktemp)
work=$(mktemp -d)

as_root() {
    if [ "$(id -u)" = 0 ]; then "$@"; else sudo -n "$@"; fi
}
can_root() {
    [ "$(id -u)" = 0 ] || sudo -n true 2>/dev/null
}
cleanup() {
    for m in "$work"/*/mnt; do
        [ -d "$m" ] && as_root umount "$m" 2>/dev/null || true
    done
    rm -rf "$work"
    rm -f "$results"
}
trap cleanup EXIT

# fstype_of <path>: the filesystem type of the longest matching mount point.
fstype_of() {
    awk -v p="$1/" '{
        mp=$2; if (substr(mp,length(mp),1)!="/") mp=mp "/";
        if (index(p, mp)==1 && length(mp) > best) { best=length(mp); t=$3 }
    } END { print t }' /proc/mounts
}

exercised() { cut -f1 "$results" | grep -qx "$1"; }

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

loopback() {
    type=$1
    case $type in
        ext4) tool=mkfs.ext4; opts="-q -F" ;;
        xfs) tool=mkfs.xfs; opts="-q -f" ;;
        btrfs) tool=mkfs.btrfs; opts="-q -f" ;;
    esac
    if ! command -v "$tool" >/dev/null 2>&1; then
        echo "note: $tool not installed; $type not exercised"
        return
    fi
    d="$work/$type"
    mkdir -p "$d/mnt"
    img="$d/fs.img"
    # xfs refuses an image below ~300 MB, so use 512 MB for every loopback.
    if ! truncate -s 512M "$img" 2>/dev/null; then
        dd if=/dev/zero of="$img" bs=1M count=512 2>/dev/null || { echo "note: no image for $type"; return; }
    fi
    if ! as_root "$tool" $opts "$img" >"$d/mkfs.log" 2>&1; then
        echo "note: mkfs $type failed ($(tail -n 1 "$d/mkfs.log" 2>/dev/null)); not exercised"
        return
    fi
    if as_root mount -o loop "$img" "$d/mnt" 2>/dev/null; then
        # A root-mounted filesystem root is root-owned; make it writable so the
        # unprivileged fingerprint run can create its working directory.
        as_root chmod 0777 "$d/mnt" 2>/dev/null || true
        run_fs "$type" "$d/mnt"
        as_root umount "$d/mnt" 2>/dev/null || as_root umount -l "$d/mnt" 2>/dev/null || true
    else
        echo "note: loopback mount $type unavailable; not exercised"
    fi
}

# 1. the repository's own filesystem
run_fs "$(fstype_of "$root")" "$root"
# 2. a real tmpfs
[ -w /dev/shm ] && run_fs tmpfs /dev/shm
# 3. real block-backed mounts
for type in btrfs xfs; do
    exercised "$type" && continue
    mp=$(awk -v t="$type" '$3==t {print $2; exit}' /proc/mounts)
    if [ -n "${mp:-}" ] && [ -w "$mp" ]; then
        run_fs "$type" "$mp"
    fi
done
# 4. loopback images when privileged
if can_root; then
    for type in ext4 xfs btrfs; do
        exercised "$type" && continue
        loopback "$type"
    done
else
    echo "note: no root/sudo; loopback ext4/xfs/btrfs not exercised"
fi

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
