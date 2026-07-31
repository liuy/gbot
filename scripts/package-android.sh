#!/usr/bin/env bash
# Build app/android APK with embedded gbot binary + Termux bootstrap.
#
# Bootstrap zip is used as-is (no re-compress). gbot and rg binaries are
# packaged as separate assets, injected into usr/bin/ by BootstrapInstaller
# at runtime.
#
# Output: dist/gbot-local-<version>.apk
set -euo pipefail

VERSION="${1:-0.0.0-dev}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIST="${ROOT}/dist"
CACHE="${HOME}/.gbot/cache/package-android"
ANDROID_DIR="${ROOT}/app/android"
APP_DIR="${ANDROID_DIR}/app"
ASSETS="${APP_DIR}/src/main/assets"

BOOTSTRAP_TAG="bootstrap-2026.07.05-r1+apt.android-7"
BOOTSTRAP_URL="https://github.com/termux/termux-packages/releases/download/${BOOTSTRAP_TAG}/bootstrap-aarch64.zip"
BOOTSTRAP_CACHE="${CACHE}/bootstrap-aarch64.zip"

RG_VERSION="15.2.0"
RG_URL="https://github.com/BurntSushi/ripgrep/releases/download/${RG_VERSION}/ripgrep-${RG_VERSION}-aarch64-unknown-linux-musl.tar.gz"
RG_CACHE="${CACHE}/ripgrep-${RG_VERSION}-aarch64-musl.tar.gz"

require() { command -v "$1" >/dev/null 2>&1 || { echo "ERROR: $1 not found. $2" >&2; exit 1; }; }
require curl "Install curl"
require unzip "Install unzip"

mkdir -p "${CACHE}" "${DIST}" "${ASSETS}"

# 1. Build the WUI frontend
echo "Building WUI frontend..."
cd "${ROOT}/web/ui" && npm install && npm run build
gzip -kf "${ROOT}/pkg/connector/wui/assets/index.html"

# 2. Download Termux bootstrap (cached, used as-is)
if [ ! -f "${BOOTSTRAP_CACHE}" ]; then
    echo "Downloading Termux bootstrap ${BOOTSTRAP_TAG}..."
    curl -fL --retry 3 -o "${BOOTSTRAP_CACHE}" "${BOOTSTRAP_URL}"
fi
cp "${BOOTSTRAP_CACHE}" "${ASSETS}/bootstrap-aarch64.zip"
echo "Staged assets/bootstrap-aarch64.zip ($(du -h "${ASSETS}/bootstrap-aarch64.zip" | cut -f1))"

# 3. Download rg (static musl binary)
if [ ! -f "${RG_CACHE}" ]; then
    echo "Downloading ripgrep ${RG_VERSION} aarch64-musl..."
    curl -fL --retry 3 -o "${RG_CACHE}" "${RG_URL}"
fi
TMP=$(mktemp -d); trap 'rm -rf "${TMP}"' EXIT
tar -xzf "${RG_CACHE}" -C "${TMP}"
RG_BIN=$(find "${TMP}" -name rg -type f | head -1)
if [ -z "${RG_BIN}" ]; then
    echo "ERROR: rg not found in tarball" >&2; exit 1
fi
cp "${RG_BIN}" "${ASSETS}/rg-arm64"
chmod +x "${ASSETS}/rg-arm64"
echo "Staged assets/rg-arm64 ($(du -h "${ASSETS}/rg-arm64" | cut -f1))"

# 4. Cross-compile gbot as arm64-linux binary
echo "Building gbot arm64 binary..."
cd "${ROOT}"
NDK_VERSION="${NDK_VERSION:-26.3.11579264}"
MIN_SDK="21"
SDK_ROOT="${ANDROID_HOME:-${ANDROID_SDK_ROOT:-$HOME/Android/Sdk}}"
NDK_ROOT="${ANDROID_NDK_HOME:-${SDK_ROOT}/ndk/${NDK_VERSION}}"
if [ ! -d "${NDK_ROOT}" ]; then
    NDK_ROOT=$(ls -d "${SDK_ROOT}"/ndk/* 2>/dev/null | sort -V | tail -1 || true)
fi
case "$(uname -s)" in
    Darwin) HOST_TAG="darwin-x86_64" ;;
    Linux)  HOST_TAG="linux-x86_64" ;;
    *)      echo "ERROR: unsupported host OS" >&2; exit 1 ;;
esac
TOOLCHAIN="${NDK_ROOT}/toolchains/llvm/prebuilt/${HOST_TAG}"
export CC="${TOOLCHAIN}/bin/aarch64-linux-android${MIN_SDK}-clang"
export CXX="${TOOLCHAIN}/bin/aarch64-linux-android${MIN_SDK}-clang++"
export CGO_ENABLED=1
export GOOS=android
export GOARCH=arm64
go build -tags android,production,netcgo \
    -trimpath -buildvcs=false -ldflags="-w -s" \
    -o "${ASSETS}/gbot-arm64" \
    ./cmd/wails/
chmod +x "${ASSETS}/gbot-arm64"
echo "Staged assets/gbot-arm64 ($(du -h "${ASSETS}/gbot-arm64" | cut -f1))"

# 5. Remove jniLibs (no longer needed — targetSdk 28 allows exec from filesDir)
rm -rf "${APP_DIR}/src/main/jniLibs"

# 6. Build APK via Gradle — --rerun-tasks forces repackaging so assets
# (gbot/rg/bootstrap) are always fresh; without it gradle's incremental
# cache can serve a stale APK with old binaries.
echo "Running gradle assembleDebug..."
cd "${ANDROID_DIR}"
chmod +x ./gradlew
./gradlew assembleDebug --rerun-tasks
APK="${APP_DIR}/build/outputs/apk/debug/app-debug.apk"
if [ ! -f "${APK}" ]; then
    echo "ERROR: expected APK at ${APK}" >&2; exit 1
fi

# 7. Stage into dist/
FINAL="${DIST}/gbot-local-${VERSION}.apk"
cp "${APK}" "${FINAL}"
echo ""
echo "Built: ${FINAL}"
ls -lh "${FINAL}"
