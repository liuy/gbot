#!/usr/bin/env bash
# Build gbot's Windows NSIS installer:
#   dist/gbot.exe  (NSIS installer, not the raw binary)
# Bundle layout: gbot.exe (Wails binary) at root, PortableGit unpacked
# as-is, rg.exe in bin/. NSIS handles install/uninstall/registry natively.
# NSIS compiles the installer from build/gbot.nsi.
# Cache: ~/.gbot/cache/package/ (versioned filenames, never auto-cleaned).
set -euo pipefail

VERSION="${1:-0.0.0-dev}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BUILD="${ROOT}/build"
STAGING="${BUILD}/staging"
DIST="${ROOT}/dist"
CACHE="${HOME}/.gbot/cache/package"

PORTABLEGIT_URL="https://github.com/git-for-windows/git/releases/download/v2.55.0.windows.3/PortableGit-2.55.0.3-64-bit.7z.exe"
PORTABLEGIT_CACHE="${CACHE}/PortableGit-2.55.0.3.7z.exe"
RIPGREP_URL="https://github.com/BurntSushi/ripgrep/releases/download/15.2.0/ripgrep-15.2.0-x86_64-pc-windows-msvc.zip"
RIPGREP_CACHE="${CACHE}/ripgrep-15.2.0-x86_64-pc-windows-msvc.zip"

require() { command -v "$1" >/dev/null 2>&1 || { echo "ERROR: $1 not found. $2" >&2; exit 1; }; }
require 7z       "Install p7zip-full (Debian: sudo apt install p7zip-full)"
require curl     "Install curl"
require unzip    "Install unzip"
require makensis "Install NSIS (Debian: sudo apt install nsis)"

# Detect NSIS plugin directory (Linux + Windows paths)
NSIS_HOME=""
for d in "/usr/share/nsis" "/usr/local/share/nsis" "C:/Program Files (x86)/NSIS" "C:/Program Files/NSIS"; do
    if [ -d "$d/Plugins" ]; then NSIS_HOME="$d"; break; fi
done
if [ -z "$NSIS_HOME" ]; then
    echo "ERROR: NSIS installation not found" >&2; exit 1
fi

# Prerequisite: EnVar NSIS plugin must be installed (for PATH manipulation).
# Linux: download EnVar_plugin.zip from https://nsis.sourceforge.io/EnVar_plug-in
#   sudo unzip EnVar_plugin.zip -d /usr/share/nsis/
# Windows: extract to C:\Program Files (x86)\NSIS\
if ! ls "${NSIS_HOME}/Plugins/x86-unicode/EnVar.dll" >/dev/null 2>&1 \
   && ! ls "${NSIS_HOME}/Plugins/amd64-unicode/EnVar.dll" >/dev/null 2>&1; then
    echo "ERROR: EnVar NSIS plugin not found in ${NSIS_HOME}/Plugins/" >&2
    echo "  Download: https://nsis.sourceforge.io/EnVar_plug-in" >&2
    echo "  Extract to ${NSIS_HOME}/" >&2
    exit 1
fi

mkdir -p "${CACHE}"
rm -rf "${STAGING}" "${DIST}"
mkdir -p "${STAGING}" "${DIST}"

# 1. Build frontend
echo "Building WUI frontend..."
cd "${ROOT}/web/ui" && npm install && npm run build
gzip -kf "${ROOT}/pkg/connector/wui/assets/index.html"

# 2. Build gbot.exe (cross-compile from Linux, or native on Windows)
echo "Building gbot.exe..."
cd "${ROOT}"
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o "${STAGING}/gbot.exe" ./cmd/wails/

# 3. Download + extract PortableGit
if [ ! -f "${PORTABLEGIT_CACHE}" ]; then
    echo "Downloading PortableGit 2.55.0.3..."
    curl -fL --retry 3 -o "${PORTABLEGIT_CACHE}" "${PORTABLEGIT_URL}"
fi
echo "Extracting PortableGit..."
7z x -o"${STAGING}" -y "${PORTABLEGIT_CACHE}" >/dev/null
rm -f "${STAGING}/git-bash.exe" "${STAGING}/git-cmd.exe" "${STAGING}/post-install.bat" "${STAGING}/LICENSE.txt" "${STAGING}/README.portable"

# 4. Download + stage ripgrep
if [ ! -f "${RIPGREP_CACHE}" ]; then
    echo "Downloading ripgrep 15.2.0..."
    curl -fL --retry 3 -o "${RIPGREP_CACHE}" "${RIPGREP_URL}"
fi
RG_TMP="$(mktemp -d)"
trap 'rm -rf "${RG_TMP}"' EXIT
unzip -o -q "${RIPGREP_CACHE}" -d "${RG_TMP}"
RG_EXE="$(find "${RG_TMP}" -type f -name 'rg.exe' | head -n1)"
if [ -z "${RG_EXE}" ]; then
    echo "ERROR: rg.exe not found in ${RIPGREP_CACHE}" >&2
    exit 1
fi
cp "${RG_EXE}" "${STAGING}/bin/rg.exe"

# 5. Verify bundle
echo "Verifying bundle contents..."
for f in gbot.exe bin/bash.exe bin/rg.exe bin/git.exe usr/bin/ls.exe mingw64/bin/git.exe; do
    [ -f "${STAGING}/${f}" ] || { echo "ERROR: missing ${f}" >&2; exit 1; }
done

# 6. Build NSIS installer
echo "Building NSIS installer..."
makensis -V2 "${ROOT}/build/gbot.nsi"

echo ""
echo "Built: ${DIST}/gbot.exe"
ls -lh "${DIST}/gbot.exe"
