.PHONY: web-novnc web-mv all build build-debug build-android build-all build-windows build-windows-gui wails-build debug test lint mutate check clean agent-start agent-stop install app-check web-build web-test web-check web-lint web-weak package package-windows package-android

BINARY := gbot
ifeq ($(OS),Windows_NT)
	BINARY := gbot.exe
endif
BINARY_DEBUG := gbot-debug
CMD := ./cmd/gbot/
PKG := ./pkg/...
ALL := ./pkg/... ./cmd/...
GBOT_HOME := $(HOME)/.gbot
VERSION ?= 0.0.0-dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X 'github.com/liuy/gbot/pkg/app.version=$(VERSION)' -X 'github.com/liuy/gbot/pkg/app.commit=$(COMMIT)'

# -N: disable optimization (keeps locals alive for inspection)
# -l: disable inlining (preserves real call frames)
DEBUG_GCFLAGS := -gcflags="all=-N -l"

all: build
	./$(BINARY)

# build compiles frontend (web/ui) + backend (Go binary).
build: web-build
	go build -ldflags="$(LDFLAGS)" -o $(BINARY) $(CMD)

# build-android compiles a binary that can replace the GBot APK's gbot
# on-device (Termux). Run this on the phone, then cp to /usr/bin/gbot.
build-android: web-build
	CGO_ENABLED=1 go build -tags android,production,netcgo \
		-trimpath -ldflags="-w -s $(LDFLAGS)" \
		-o gbot-android ./cmd/gbot/

build-debug:
	go build $(DEBUG_GCFLAGS) -o $(BINARY_DEBUG) $(CMD)

# build-windows cross-compiles shared code for windows/amd64.
# Catches any Unix-only symbol leaking into shared bash code.
build-windows:
	GOOS=windows GOARCH=amd64 go build ./pkg/... ./cmd/gbot/

# build-windows-gui cross-compiles the Windows GUI entry point (wails window
# via gui_windows.go build tag). May fail on non-Windows hosts due to Wails
# CGO/webkitgtk deps. Uses leading '-' so failure is non-fatal in `make check`.
build-windows-gui:
	-GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o /dev/null ./cmd/gbot/ 2>/dev/null || \
		echo "NOTE: cmd/gbot windows cross-compile skipped (build on Windows for production)"

# icon.ico is auto-generated from icon.png so users only maintain one file.
# PIL generates multi-resolution ICO with proper RGBA alpha and PNG frames.
$(CMD)icon.ico $(CMD)rsrc_windows_amd64.syso: $(CMD)icon.png scripts/gen-ico.sh
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
	go test $(PKG) ./cmd/... -count=1 -timeout 180s -coverprofile=coverage.out
else
	go test $(PKG) ./cmd/... -race -count=1 -timeout 180s -coverprofile=coverage.out
endif
	go test ./test/ -count=1 -timeout 180s
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

# mutate rewrites one expression at a time on the lines changed since MUTATE_BASE
# and re-runs the tests: an escaped mutant is code no test asserts, which coverage
# cannot show. Standalone on purpose -- it is NOT in CHECK_TARGETS.
#
# The mutant numbers below are for MUTATE_BASE=HEAD~1 (one commit): 60 mutants --
# --dry-run's count, an upper bound, so MUTATE_MAX trips slightly early -- scoring
# 29 killed / 31 escaped, MSI 48.3%. HEAD is the default because the gate reviews
# the change about to be committed (CLAUDE.md runs it after `git add`, before `git
# commit`); an origin/master base also scores whatever other agents' commits landed
# on the shared tree in the meantime. Wall clock
# depends on the flag set: the pre-coefficient runs measured 52s on a warm build
# cache and 3m30s cold, a run with the coefficient 3m35s cold. Cost is dominated by
# escaped mutants, each of which pays the full suite of its package (~20s for
# pkg/engine) on top of recompiling the mutated one. --per-test was measured slower
# (6m45s): building the per-test coverage map does not amortize at this mutant count.
#
# The tool runs its own `go test` with -vet=off and without -race (unlike
# `make test`), so MSI says nothing about data races.
#
# --timeout-coefficient is here because a mutant that merely runs slow is scored
# KILLED: `go test` panics at its -timeout, exits 1, and exit 1 means killed, which
# inflates MSI. Without the coefficient the per-mutant timeout is the 10s
# --exec-timeout default, which is below the clean run of the slower packages here;
# 3 gives each mutant 3x the slowest targeted package's measured clean run, so no
# hand-tuned number goes stale as the suite grows. The green-baseline pre-flight
# uses neither: it gets a fixed 300s and exits 3 when a package is not already
# green. No --min-msi: a handful of mutants makes the percentage meaningless (both
# thresholds default to -1, so --ignore-msi-with-no-mutations would suppress
# nothing).
#
# The recipe must stay ONE \-continued command: `exit 0` in its own recipe line
# ends only that line's shell and make runs the next line anyway, which would drop
# the skip guards straight into go-mutesting.
MUTATE_BASE ?= HEAD
MUTATE_MAX ?= 400
# The tool's own default is all 72 CPUs on this box; 16 caps contention, it is not a
# speed win. Tunable: make mutate MUTATE_WORKERS=8.
MUTATE_WORKERS ?= 16
mutate:
	@command -v go-mutesting >/dev/null 2>&1 || { \
	  echo "NOTE: go-mutesting not installed, skipping mutation testing"; \
	  echo '      go install github.com/jonbaldie/go-mutesting/v2/cmd/go-mutesting@latest'; \
	  exit 0; }; \
	git rev-parse --verify -q '$(MUTATE_BASE)' >/dev/null || { \
	  echo "NOTE: $(MUTATE_BASE) not found, skipping (make mutate MUTATE_BASE=<ref>)"; \
	  exit 0; }; \
	n=$$(go-mutesting --git-diff-lines --git-diff-base='$(MUTATE_BASE)' --dry-run $(PKG) | awk '/^Total:/{print $$2}'); \
	if [ "$${n:-0}" -eq 0 ]; then \
	  echo "NOTE: no mutants on the lines changed since $(MUTATE_BASE), nothing to mutate"; \
	  exit 0; \
	fi; \
	if [ "$${n:-0}" -gt $(MUTATE_MAX) ] && [ -z "$(FORCE)" ]; then \
	  echo "NOTE: $${n:-0} mutants on the changed lines (> $(MUTATE_MAX)), skipping"; \
	  echo "      make mutate FORCE=1 to run them anyway"; \
	  exit 0; \
	fi; \
	go-mutesting --git-diff-lines --git-diff-base='$(MUTATE_BASE)' \
	  --workers $(MUTATE_WORKERS) --no-diffs --quiet \
	  --timeout-coefficient=3 $(PKG)

check: $(CHECK_TARGETS)
	@echo "✓ ALL CHECKS PASSED"

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

package-windows: $(CMD)icon.ico
	bash scripts/package-wails.sh $(VERSION)

# package-android builds the self-contained Android APK. Requires Android
# SDK + NDK 26.3.x + JDK 21. Sideload only (targetSdk 28 for W^X exemption).
package-android: build-android
	bash scripts/package-android.sh $(VERSION)

wails-build: web-build
	go build -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/gbot/

# e2e
agent-start: build
	./gbot-agent start --no-build

agent-stop:
	./gbot-agent stop

install: build
	@mkdir -p $(GBOT_HOME)/bin $(GBOT_HOME)/agents $(GBOT_HOME)/skills
	@cp $(BINARY) $(GBOT_HOME)/bin/.$(BINARY).tmp && mv $(GBOT_HOME)/bin/.$(BINARY).tmp $(GBOT_HOME)/bin/$(BINARY)
	@echo "Installed binary to $(GBOT_HOME)/bin/$(BINARY)"
	@if command -v rg >/dev/null 2>&1; then \
		echo "rg: $$(rg --version | head -1)"; \
	else \
		echo "WARNING: ripgrep not found (gbot search tools depend on it)."; \
		echo "  debian/ubuntu: apt install ripgrep"; \
		echo "  fedora/rhel:   dnf install ripgrep"; \
		echo "  arch:          pacman -S ripgrep"; \
	fi
	@missing=""; \
	for cmd in ls cat cp mv rm mkdir sed awk; do \
		command -v $$cmd >/dev/null 2>&1 || missing="$$missing $$cmd"; \
	done; \
	if [ -n "$$missing" ]; then \
		echo "WARNING: missing base commands:$$missing — install coreutils."; \
	fi
	@if ! echo ":$$PATH:" | grep -q ":$(GBOT_HOME)/bin:"; then \
		for rc in $$HOME/.bashrc $$HOME/.zshrc; do \
			[ -f "$$rc" ] || continue; \
			grep -q 'PATH=.*\.gbot/bin' "$$rc" && continue; \
			printf '\n# added by gbot install\nexport PATH="%s/bin:$$PATH"\n' "$(GBOT_HOME)" >> "$$rc"; \
			echo "PATH: appended $(GBOT_HOME)/bin to $$rc (reopen shell to apply)"; \
		done; \
	fi
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
	@echo "Done. Run 'gbot'."

# Android app checks (lint + test)
app-check:
	$(MAKE) -C app/android check

# web-build regenerates the React SPA embedded into the Go binary.
# Assets are checked into pkg/connector/wui/assets/ so go build works
# without Node. Run this after changing web/ui source.
web-build: web-mv-gen
	cd web/ui && export LD_PRELOAD="$${LD_PRELOAD:-$${PREFIX:+$$PREFIX/lib/libtermux-exec.so}}" && npm ci && npm run build
	gzip -kf pkg/connector/wui/assets/index.html

web-test:
	cd web/ui && npm test

web-check: web-build web-test web-lint web-weak web-novnc web-mv

web-novnc:
	cd web/ui && npm run check:novnc

web-mv:
	cd web/ui && npm run check:mv
# Regenerates (not checks) the model-viewer bundle so `make build` can never
# embed a stale one — fork source edits need no remembered pre-step.
web-mv-gen:
	cd web/ui && npm run build:mv

web-lint:
	cd web/ui && npm run lint

web-weak:
	cd web/ui && npx vitest run test/weak.test.ts
