#!/usr/bin/env bash
# Build a release zip on Windows (Git Bash / MSYS2).
#   bash build-release.sh [version]
# Optional signing: export SIGN_PFX=/path/to/cert.pfx SIGN_PASSWORD=...
set -euo pipefail
cd "$(dirname "$0")"

VERSION="${1:-$(git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//' || echo 0.0.0)}"
PKG="diskmon-${VERSION}-win64"

if ! command -v go >/dev/null 2>&1; then
  echo "go not found in PATH (need Go 1.27+)" >&2
  exit 1
fi

echo "== 1/5 build app =="
mkdir -p dist
go build -trimpath -ldflags "-H windowsgui -s -w -X main.version=${VERSION}" -o diskmon-go.exe .

echo "== 2/5 prepare installer payload =="
mkdir -p installer/payload
cp -f diskmon-go.exe installer/payload/diskmon-go.exe
cp -f diskmon.ico installer/payload/icon.ico
cp -f README.md installer/payload/README.md
if [ -f "${SIGN_CER:-}" ]; then
  cp -f "$SIGN_CER" installer/payload/diskmon-codesign.cer
elif [ ! -f installer/payload/diskmon-codesign.cer ]; then
  : > installer/payload/diskmon-codesign.cer
fi

echo "== 3/5 build installer =="
go build -trimpath -ldflags "-H windowsgui -s -w" -o dist/diskmon-setup.exe ./installer

echo "== 4/5 sign (optional) =="
SIGNTOOL="$(command -v signtool.exe || true)"
if [ -n "${SIGN_PFX:-}" ] && [ -n "${SIGN_PASSWORD:-}" ] && [ -n "$SIGNTOOL" ]; then
  for f in dist/diskmon-setup.exe diskmon-go.exe; do
    "$SIGNTOOL" sign /f "$SIGN_PFX" /p "$SIGN_PASSWORD" /fd sha256 \
      /tr http://timestamp.digicert.com /td sha256 "$f"
  done
else
  echo "skip signing (set SIGN_PFX/SIGN_PASSWORD or install signtool to enable)"
fi

echo "== 5/5 assemble $PKG =="
rm -rf "release/$PKG"
mkdir -p "release/$PKG"
cp -f dist/diskmon-setup.exe "release/$PKG/"
cp -f diskmon-go.exe "release/$PKG/"
cp -f README.md "release/$PKG/"
[ -f README.zh-CN.md ] && cp -f README.zh-CN.md "release/$PKG/"
[ -f "${SIGN_CER:-}" ] && cp -f "$SIGN_CER" "release/$PKG/"

(
  cd "release/$PKG"
  sha256sum diskmon-setup.exe diskmon-go.exe > SHA256SUMS.txt 2>/dev/null || \
    certutil -hashfile diskmon-setup.exe SHA256 > SHA256SUMS.txt
)
echo "release/$PKG ready"
