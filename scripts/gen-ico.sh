#!/bin/bash
# Generate icon.ico and rsrc_windows_amd64.syso from icon.png
# using Wails v3 official CLI commands (no PIL, no go-winres).
set -e
DIR="$(dirname "$0")/../cmd/wails"

# Step 1: PNG → ICO (multi-resolution, Wails handles sizing)
wails3 generate icons -input "${DIR}/icon.png" -windowsfilename "${DIR}/icon.ico"

# Step 2: ICO + manifest → syso (Windows PE resource)
wails3 generate syso -arch amd64 \
  -icon "${DIR}/icon.ico" \
  -manifest "${DIR}/gbot.manifest" \
  -out "${DIR}/rsrc_windows_amd64.syso"
