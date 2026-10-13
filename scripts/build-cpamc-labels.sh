#!/usr/bin/env bash
set -euo pipefail
repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
output=${1:?Usage: build-cpamc-labels.sh /path/to/management.html}
output=$(realpath -m -- "$output")
work=$(mktemp -d /tmp/cpamc-label-build.XXXXXX)
cleanup() {
  # Bun's Docker image can create root-owned files in the temporary checkout.
  docker run --rm --network none -v "$work:/cleanup" oven/bun:1.3.14 \
    sh -c 'rm -rf /cleanup/* /cleanup/.[!.]* /cleanup/..?*' >/dev/null 2>&1 || true
  rmdir "$work" 2>/dev/null || true
}
trap cleanup EXIT
git clone --depth 1 --branch v1.24.2 \
  https://github.com/router-for-me/Cli-Proxy-API-Management-Center.git "$work/source"
git -C "$work/source" apply "$repo_root/patches/cpamc-v1.24.2-workbuddy-labels.patch"
docker run --rm -v "$work/source:/src" -w /src oven/bun:1.3.14 \
  bun install --frozen-lockfile
docker run --rm --network none -v "$work/source:/src" -w /src oven/bun:1.3.14 \
  bun run verify
mkdir -p -- "$(dirname -- "$output")"
cp -- "$work/source/dist/index.html" "$output"
printf 'Built %s\n' "$output"
