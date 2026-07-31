.PHONY: all build build-debug build-android build-all build-windows build-windows-gui wails-build debug test lint check clean agent-start agent-stop install app-check web-build web-test web-check web-lint web-weak package package-windows package-android

BINARY := gbot
ifeq ($(OS),Windows_NT)
	BINARY := gbot.exe
endif
BINARY_DEBUG := gbot-debug
CMD := ./cmd/gbot/
WCMD := ./cmd/wails/
PKG := ./pkg/...
ALL := ./pkg/... ./cmd/...
GBOT_HOME := $(HOME)/.gbot
VERSION ?= 0.0.0-dev

# -N: disable optimization (keeps locals alive for inspection)
# -l: disable inlining (preserves real call frames)
DEBUG_GCFLAGS := -gcflags="all=-N -l"

all: build
	./$(BINARY)

# build compiles frontend (web/ui) + backend (Go binary).
build: web-build
	go build -o $(BINARY) $(CMD)

# build-android compiles a binary that can replace the GBot APK's gbot
# on-device (Termux). Uses wails entry point + android tags to match
# the production build. Run this on the phone, then cp to /usr/bin/gbot.
build-android: web-build
	CGO_ENABLED=1 go build -tags android,production,netcgo \
		-trimpath -ldflags="-w -s" \
		-o gbot-android ./cmd/gbot/

build-debug:
	go build $(DEBUG_GCFLAGS) -o $(BINARY_DEBUG) $(CMD)

# build-windows cross-compiles shared code for windows/amd64.
# Catches any Unix-only symbol leaking into shared bash code.
build-windows:
	GOOS=windows GOARCH=amd64 go build ./pkg/... ./cmd/gbot/

# build-windows-gui cross-compiles the Wails entry point separately.
# May fail on non-Windows hosts due to Wails CGO/webkitgtk deps.
# Uses leading '-' so failure is non-fatal in `make check`.
build-windows-gui:
	-GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o /dev/null ./cmd/wails/ 2>/dev/null || \
		echo "NOTE: cmd/wails cross-compile skipped (build on Windows for production)"

# icon.ico is auto-generated from icon.png so users only maintain one file.
# PIL generates multi-resolution ICO with proper RGBA alpha and PNG frames.
$(WCMD)/icon.ico $(WCMD)/rsrc_windows_amd64.syso: $(WCMD)/icon.png scripts/gen-ico.sh
	bash scripts/gen-ico.sh

# Alias for clarity when only one is wanted.
build-all: build

# debug launches gbot-debug under dlv (interactive REPL).
# gbot runs as a child of dlv, so ptrace_scope=1 is fine.
# Connect from another terminal with: dlv connect :2345
debug: build-debug
	dlv exec ./$(BINARY_DEBUG) --headless --api-version=2 --listen=127.0.0.1:2345

test:
ifeq ($(shell go env GOARCH),arm64)
	go test $(PKG) -count=1 -timeout 120s -coverprofile=coverage.out
else
	go test $(PKG) -race -count=1 -timeout 120s -coverprofile=coverage.out
endif
	go test ./test/ -count=1 -timeout 120s
	cd web/ui && npm test
	@echo ""
	@echo "Coverage:"
	@go tool cover -func=coverage.out
	@echo ""
	@echo "Total coverage:"
	@go tool cover -func=coverage.out | tail -1
	@rm -f coverage.out

lint:
	golangci-lint run $(ALL)

ifeq ($(shell go env GOOS),android)
CHECK_TARGETS := build test lint fix web-lint web-weak
else
CHECK_TARGETS := build build-windows build-windows-gui test lint fix web-lint web-weak
endif

check: $(CHECK_TARGETS)

fix:
	@gofmt -w $(shell find ./pkg ./cmd -name '*.go')
	go fix ./pkg/... ./cmd/... 2>/dev/null || true

clean:
	rm -f $(BINARY) $(BINARY_DEBUG) coverage.out *.out *.prof *.test
	rm -f /tmp/gbot-screen.raw /tmp/gbot-agent.pid /tmp/gbot-input
	rm -rf /tmp/Test*
	screen -S gbot -X quit 2>/dev/null || true
	go clean
	@echo "cleaned"

# package builds the Windows NSIS installer into dist/.
# Linux/macOS packaging is future work.
package: package-windows

package-windows: $(WCMD)/icon.ico
	bash scripts/package-wails.sh $(VERSION)

# package-android builds the self-contained Android APK. Requires Android
# SDK + NDK 26.3.x + JDK 21. Sideload only (targetSdk 28 for W^X exemption).
package-android:
	bash scripts/package-android.sh $(VERSION)

wails-build: web-build
	go build -o $(BINARY) ./cmd/wails/

# e2e
agent-start: build
	./gbot-agent start --no-build

agent-stop:
	./gbot-agent stop

install: build
	@mkdir -p $(GBOT_HOME)/agents $(GBOT_HOME)/skills
	@if [ -d agents ]; then \
		cp agents/*.md $(GBOT_HOME)/agents/; \
		echo "Installed agents to $(GBOT_HOME)/agents/"; \
	fi
	@for dir in skills/*/; do \
		if [ -f "$${dir}SKILL.md" ]; then \
			skill_name=$$(basename $$dir); \
			mkdir -p $(GBOT_HOME)/skills/$$skill_name; \
			cp $${dir}SKILL.md $(GBOT_HOME)/skills/$$skill_name/; \
			echo "Installed skill: $$skill_name"; \
		fi; \
	done
	@echo "Done. Run 'gbot' to use /plan, /execute, /review, /goal."

# Android app checks (lint + test)
app-check:
	$(MAKE) -C app/android check

# web-build regenerates the React SPA embedded into the Go binary.
# Assets are checked into pkg/connector/wui/assets/ so go build works
# without Node. Run this after changing web/ui source.
web-build:
	cd web/ui && npm install && npm run build
	gzip -kf pkg/connector/wui/assets/index.html

web-test:
	cd web/ui && npm test

web-check: web-build web-test web-lint web-weak

web-lint:
	cd web/ui && npm run lint

web-weak:
	cd web/ui && npx vitest run test/weak.test.ts
