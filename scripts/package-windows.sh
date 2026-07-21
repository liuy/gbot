#!/usr/bin/env bash
# Build gbot's Windows portable zip:
#   dist/gbot-windows-x64-<version>.zip
# Bundle layout: gbot.exe at root, PortableGit unpacked as-is, rg.exe in bin/.
# Cache: ~/.gbot/cache/package/ (versioned filenames, never auto-cleaned).
set -euo pipefail

VERSION="${1:-0.0.0-dev}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIST="${ROOT}/dist"
STAGING="${DIST}/staging"
CACHE="${HOME}/.gbot/cache/package"

PORTABLEGIT_URL="https://github.com/git-for-windows/git/releases/download/v2.55.0.windows.3/PortableGit-2.55.0.3-64-bit.7z.exe"
PORTABLEGIT_CACHE="${CACHE}/PortableGit-2.55.0.3.7z.exe"
RIPGREP_URL="https://github.com/BurntSushi/ripgrep/releases/download/15.2.0/ripgrep-15.2.0-x86_64-pc-windows-msvc.zip"
RIPGREP_CACHE="${CACHE}/ripgrep-15.2.0-x86_64-pc-windows-msvc.zip"

ZIP_NAME="gbot-windows-x64-${VERSION}.zip"

require() {
    command -v "$1" >/dev/null 2>&1 || {
        echo "ERROR: $1 not found. $2" >&2
        exit 1
    }
}
require 7z        "Install p7zip-full (Debian: sudo apt install p7zip-full)."
require goreleaser "Install: go install github.com/goreleaser/goreleaser/v2@latest"
require curl      "Install curl."
require unzip     "Install unzip."
require zip       "Install zip."

mkdir -p "${CACHE}"
rm -rf "${DIST}"
mkdir -p "${DIST}"

if [ ! -f "${PORTABLEGIT_CACHE}" ]; then
    echo "Downloading PortableGit 2.55.0.3..."
    curl -fL --retry 3 -o "${PORTABLEGIT_CACHE}" "${PORTABLEGIT_URL}"
fi
if [ ! -f "${RIPGREP_CACHE}" ]; then
    echo "Downloading ripgrep 15.2.0..."
    curl -fL --retry 3 -o "${RIPGREP_CACHE}" "${RIPGREP_URL}"
fi

echo "Building gbot.exe via GoReleaser (snapshot, single-target)..."
cd "${ROOT}"
GOOS=windows GOARCH=amd64 goreleaser build --snapshot --clean --single-target

GBOT_EXE="$(find "${DIST}" -type f -name 'gbot.exe' | head -n1)"
if [ -z "${GBOT_EXE}" ]; then
    echo "ERROR: gbot.exe not found under ${DIST} after goreleaser build." >&2
    exit 1
fi

mkdir -p "${STAGING}"
echo "Extracting PortableGit..."
7z x -o"${STAGING}" -y "${PORTABLEGIT_CACHE}" >/dev/null

# PortableGit ships launchers, docs, and post-install scripts that we don't
# need: gbot has its own TUI (no Git Bash/CMD GUI), portable mode skips the
# registry writes that post-install.bat performs, and the bundled docs are
# PortableGit-specific (not gbot's).
rm -f "${STAGING}/git-bash.exe" \
      "${STAGING}/git-cmd.exe" \
      "${STAGING}/post-install.bat" \
      "${STAGING}/LICENSE.txt" \
      "${STAGING}/README.portable"

echo "Staging ripgrep..."
RG_TMP="$(mktemp -d)"
trap 'rm -rf "${RG_TMP}"' EXIT
unzip -o -q "${RIPGREP_CACHE}" -d "${RG_TMP}"
# BurntSushi's release zip nests files under a top-level directory
# (ripgrep-<ver>-x86_64-pc-windows-msvc/), so locate rg.exe rather than
# assuming it sits at the zip root.
RG_EXE="$(find "${RG_TMP}" -type f -name 'rg.exe' | head -n1)"
if [ -z "${RG_EXE}" ]; then
    echo "ERROR: rg.exe not found in ${RIPGREP_CACHE}" >&2
    ls -laR "${RG_TMP}" >&2
    exit 1
fi
mkdir -p "${STAGING}/bin"
cp "${RG_EXE}" "${STAGING}/bin/rg.exe"

echo "Staging gbot.exe..."
cp "${GBOT_EXE}" "${STAGING}/gbot.exe"

echo "Verifying bundle contents..."
for f in \
    "gbot.exe" \
    "bin/bash.exe" \
    "bin/rg.exe" \
    "bin/git.exe" \
    "usr/bin/ls.exe" \
    "mingw64/bin/git.exe"; do
    if [ ! -f "${STAGING}/${f}" ]; then
        echo "ERROR: missing ${f} in staging dir." >&2
        exit 1
    fi
done

(
    cd "${STAGING}"
    zip -r -q "${DIST}/${ZIP_NAME}" .
)

rm -rf "${STAGING}"

echo ""
echo "Built: ${DIST}/${ZIP_NAME}"
ls -lh "${DIST}/${ZIP_NAME}"
