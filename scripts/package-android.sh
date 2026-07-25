#!/usr/bin/env bash
# Build gbot's self-contained Android APK:
#   dist/gbot-<version>.apk
#
# Bundle layout:
#   - lib/arm64-v8a/libwails.so       (Go c-shared, gbot engine + WUI server)
#   - assets/bootstrap-aarch64.zip    (Termux bash + coreutils + bundled rg)
#
# Java unpacks the bootstrap into filesDir/usr/ on first run; Go reads
# HOME/GBOT_BASH_PATH/PATH from JNI nativeSetDataPath to find bash and rg.
#
# Cache: ~/.gbot/cache/package-android/ (never auto-cleaned).
#
# Deviations from plan: the plan invokes `wails3 task android:package`, but
# gbot's frontend lives at web/ui/ (not frontend/ as Wails v3's task system
# assumes). This script invokes gradle directly and embeds main_android.go
# in cmd/wails/ rather than via the Wails overlay mechanism.
set -euo pipefail

VERSION="${1:-0.0.0-dev}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIST="${ROOT}/dist"
CACHE="${HOME}/.gbot/cache/package-android"
ANDROID_DIR="${ROOT}/build/android"
APP_DIR="${ANDROID_DIR}/app"
ASSETS="${APP_DIR}/src/main/assets"
JNILIBS="${APP_DIR}/src/main/jniLibs/arm64-v8a"

# Termux ships bootstrap zips via termux-packages (not termux-app). This tag
# is the latest at the time of writing; bump when updating.
BOOTSTRAP_TAG="bootstrap-2026.07.05-r1+apt.android-7"
BOOTSTRAP_URL="https://github.com/termux/termux-packages/releases/download/${BOOTSTRAP_TAG}/bootstrap-aarch64.zip"
BOOTSTRAP_CACHE="${CACHE}/bootstrap-aarch64.zip"

RG_VERSION="15.2.0"
RG_URL="https://github.com/BurntSushi/ripgrep/releases/download/${RG_VERSION}/ripgrep-${RG_VERSION}-aarch64-unknown-linux-musl.tar.gz"
RG_CACHE="${CACHE}/ripgrep-${RG_VERSION}-aarch64-musl.tar.gz"

# Wails/Android toolchain
NDK_VERSION="${NDK_VERSION:-26.3.11579264}"
MIN_SDK="21"

require() { command -v "$1" >/dev/null 2>&1 || { echo "ERROR: $1 not found. $2" >&2; exit 1; }; }
require curl  "Install curl"
require unzip "Install unzip (Debian: sudo apt install unzip)"
require zip   "Install zip (Debian: sudo apt install zip)"

# Locate the NDK: $ANDROID_NDK_HOME, or the newest installed NDK under $ANDROID_HOME.
SDK_ROOT="${ANDROID_HOME:-${ANDROID_SDK_ROOT:-$HOME/Android/Sdk}}"
NDK_ROOT="${ANDROID_NDK_HOME:-${SDK_ROOT}/ndk/${NDK_VERSION}}"
if [ ! -d "${NDK_ROOT}" ]; then
    NDK_ROOT=$(ls -d "${SDK_ROOT}"/ndk/* 2>/dev/null | sort -V | tail -1 || true)
fi
if [ -z "${NDK_ROOT}" ] || [ ! -d "${NDK_ROOT}" ]; then
    echo "ERROR: Android NDK not found" >&2
    echo "  Install with: sdkmanager 'ndk;${NDK_VERSION}'" >&2
    echo "  Or set ANDROID_NDK_HOME" >&2
    exit 1
fi
echo "Using NDK: ${NDK_ROOT}"

case "$(uname -s)" in
    Darwin) HOST_TAG="darwin-x86_64" ;;
    Linux)  HOST_TAG="linux-x86_64" ;;
    *)      echo "ERROR: unsupported host OS: $(uname -s)" >&2; exit 1 ;;
esac
TOOLCHAIN="${NDK_ROOT}/toolchains/llvm/prebuilt/${HOST_TAG}"
CC="${TOOLCHAIN}/bin/aarch64-linux-android${MIN_SDK}-clang"
CXX="${TOOLCHAIN}/bin/aarch64-linux-android${MIN_SDK}-clang++"
if [ ! -x "${CC}" ]; then
    echo "ERROR: NDK clang not found at ${CC}" >&2
    exit 1
fi

mkdir -p "${CACHE}" "${DIST}" "${ASSETS}" "${JNILIBS}"

# 1. Build the WUI (gzip index.html for embedded serving via pkg/connector/wui)
echo "Building WUI frontend..."
cd "${ROOT}/web/ui" && npm install && npm run build
gzip -kf "${ROOT}/pkg/connector/wui/assets/index.html"

# 2. Download Termux bootstrap (cached)
if [ ! -f "${BOOTSTRAP_CACHE}" ]; then
    echo "Downloading Termux bootstrap ${BOOTSTRAP_TAG}..."
    curl -fL --retry 3 -o "${BOOTSTRAP_CACHE}" "${BOOTSTRAP_URL}"
fi

# 3. Download ripgrep musl (statically linked — runs on Android bionic)
if [ ! -f "${RG_CACHE}" ]; then
    echo "Downloading ripgrep ${RG_VERSION} aarch64-musl..."
    curl -fL --retry 3 -o "${RG_CACHE}" "${RG_URL}"
fi

# 4. Repackage bootstrap with rg injected into usr/bin/
# Actual Termux bootstrap has bin/, lib/, etc. at the archive root (no
# data/data/com.termux/files/usr/ prefix and no usr/ wrapper). BootstrapInstaller
# extracts to filesDir/usr/ and expects entries to start with usr/ or lib/.
# Reorganize: move root bin/ into usr/bin/ so the installer sees usr/bin/...
TMP=$(mktemp -d); trap 'rm -rf "${TMP}"' EXIT
mkdir -p "${TMP}/repack/usr"
unzip -q "${BOOTSTRAP_CACHE}" -d "${TMP}/repack/usr"
# After unzip, layout is repack/usr/{bin, lib, ...} — matches installer expectation.

if [ ! -d "${TMP}/repack/usr/bin" ]; then
    echo "ERROR: bootstrap has no bin/ directory after extraction" >&2
    echo "  Inspect: unzip -l ${BOOTSTRAP_CACHE}" >&2
    exit 1
fi
tar -xzf "${RG_CACHE}" -C "${TMP}"
RG_BIN=$(find "${TMP}" -name rg -type f | head -1)
if [ -z "${RG_BIN}" ]; then
    echo "ERROR: rg binary not found in ${RG_CACHE}" >&2
    exit 1
fi
cp "${RG_BIN}" "${TMP}/repack/usr/bin/rg"
chmod +x "${TMP}/repack/usr/bin/rg"

# 5. Write repackaged zip to assets/
rm -f "${ASSETS}/bootstrap-aarch64.zip"
( cd "${TMP}/repack" && zip -qr "${ASSETS}/bootstrap-aarch64.zip" . )
echo "Staged assets/bootstrap-aarch64.zip ($(du -h "${ASSETS}/bootstrap-aarch64.zip" | cut -f1))"

# 6. Cross-compile gbot to libwails.so via NDK
echo "Building libwails.so..."
cd "${ROOT}"
export CC CXX
export CGO_ENABLED=1
export GOOS=android
export GOARCH=arm64
go build -buildmode=c-shared -tags android,production \
    -trimpath -buildvcs=false -ldflags="-w -s" \
    -o "${JNILIBS}/libwails.so" \
    ./cmd/wails/

# 7. Assemble release APK via Gradle wrapper
echo "Running gradle assembleRelease..."
cd "${ANDROID_DIR}"
chmod +x ./gradlew
./gradlew assembleRelease
APK="${APP_DIR}/build/outputs/apk/release/app-release.apk"
if [ ! -f "${APK}" ]; then
    echo "ERROR: expected APK at ${APK}" >&2
    exit 1
fi

# 8. Stage into dist/
FINAL="${DIST}/gbot-${VERSION}.apk"
cp "${APK}" "${FINAL}"
echo ""
echo "Built: ${FINAL}"
ls -lh "${FINAL}"
