#!/usr/bin/env bash
# merp fork: cross-builds pgschema for the four RIDs merp packs (Merp.Pgschema) into bin/<rid>/.
# Usage: fork/build-rids.sh   (run from anywhere; needs Go 1.24, e.g. `mise x go@1.24 -- fork/build-rids.sh`)
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
export CGO_ENABLED=0
for target in linux-x64:linux/amd64 linux-arm64:linux/arm64 osx-arm64:darwin/arm64 win-x64:windows/amd64; do
  rid="${target%%:*}"; goos="${target#*:}"; goarch="${goos#*/}"; goos="${goos%/*}"
  exe=pgschema; [ "$goos" = windows ] && exe=pgschema.exe
  mkdir -p "bin/$rid"
  GOOS="$goos" GOARCH="$goarch" go build -trimpath -ldflags="-s -w" -o "bin/$rid/$exe" .
  echo "bin/$rid/$exe"
done
