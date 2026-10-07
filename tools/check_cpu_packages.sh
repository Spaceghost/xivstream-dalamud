#!/usr/bin/env bash
# Test actual Linux/amd64 package payloads, never install them on this machine.
set -Eeuo pipefail
trap 'echo "Package verification failed at line $LINENO: $BASH_COMMAND" >&2' ERR
repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
dist=$(realpath -- "${1:-$repo/dist}")
for tool in bwrap python3 rpm rpm2cpio cpio ar tar sha256sum cmp; do
    command -v "$tool" >/dev/null || { echo "Missing test dependency: $tool" >&2; exit 1; }
done
shopt -s nullglob
portable=("$dist"/*-linux-amd64.tar.gz)
rpms=("$dist"/*_linux_amd64.rpm)
debs=("$dist"/*_amd64.deb)
apks=("$dist"/*_linux_amd64.apk)
arch=("$dist"/*_linux_amd64.pkg.tar.zst)
for kind in portable rpms debs apks arch; do
    declare -n packages="$kind"
    if [[ ${#packages[@]} != 1 ]]; then
        echo "Expected exactly one Linux/amd64 $kind artifact; found ${#packages[@]}" >&2
        exit 1
    fi
done
work=$(mktemp -d "${TMPDIR:-/tmp}/xivstream-cpu-packages.XXXXXX")
echo "Isolated payloads and evidence: $work"
# Only named members go to stdout: no package-controlled paths are extracted.
tar -xOf "${portable[0]}" xivstream > "$work/portable"
tar -xOf "${portable[0]}" docs/config.md > "$work/config-portable.md"
# cpio can stop after its selected member, giving a healthy rpm2cpio producer
# SIGPIPE under pipefail. Check the complete decompression first; selected reads
# then have no producer whose exit could race the consumer's early close.
rpm2cpio "${rpms[0]}" > "$work/rpm-payload.cpio"
cpio -i --quiet --to-stdout ./usr/bin/xivstream < "$work/rpm-payload.cpio" > "$work/rpm"
cpio -i --quiet --to-stdout ./usr/share/doc/xivstream/config.md < "$work/rpm-payload.cpio" > "$work/config-rpm.md"
rpm -qp --scripts "${rpms[0]}" > "$work/rpm-scripts.txt"
test ! -s "$work/rpm-scripts.txt" || { echo 'Unexpected RPM install script' >&2; exit 1; }
deb_data=$(ar t "${debs[0]}" | sed -n '/^data\.tar\(\.[a-z0-9]*\)\?$/p')
[[ -n "$deb_data" && "$deb_data" != *$'\n'* ]] || { echo 'Ambiguous Debian payload' >&2; exit 1; }
ar p "${debs[0]}" "$deb_data" > "$work/$deb_data"
tar -xOf "$work/$deb_data" ./usr/bin/xivstream > "$work/deb"
tar -xOf "$work/$deb_data" ./usr/share/doc/xivstream/config.md > "$work/config-deb.md"
tar --ignore-zeros -xOf "${apks[0]}" usr/bin/xivstream > "$work/apk"
tar --ignore-zeros -xOf "${apks[0]}" usr/share/doc/xivstream/config.md > "$work/config-apk.md"
tar -xOf "${arch[0]}" usr/bin/xivstream > "$work/arch"
tar -xOf "${arch[0]}" usr/share/doc/xivstream/config.md > "$work/config-arch.md"
for kind in portable rpm deb apk arch; do
    test -s "$work/$kind"
    cmp "$work/portable" "$work/$kind"
    cmp "$repo/docs/config.md" "$work/config-$kind.md"
    chmod 0755 "$work/$kind"
    install_path=/usr/bin/xivstream
    [[ "$kind" != portable ]] || install_path=/usr/local/bin/xivstream
    python3 "$repo/tools/test_cpu_policy_package.py" --binary "$work/$kind" \
        --install-path "$install_path" 2>&1 | tee "$work/$kind-test.log"
done
sha256sum "$work/portable" "$work/rpm" "$work/deb" "$work/apk" "$work/arch"
echo "PASS: five identical package binaries; CPU-only CLI regression passed in isolated fake hosts."
