.PHONY: all build build-debug build-all debug test lint check clean agent-start agent-stop install

BINARY := gbot
BINARY_DEBUG := gbot-debug
CMD := ./cmd/gbot/
PKG := ./pkg/...
ALL := ./pkg/... ./cmd/...
GBOT_HOME := $(HOME)/.gbot

# -N: disable optimization (keeps locals alive for inspection)
# -l: disable inlining (preserves real call frames)
DEBUG_GCFLAGS := -gcflags="all=-N -l"

all: build
	./$(BINARY)

# build compiles both the optimized gbot and the dlv-friendly gbot-debug.
# Use `make build-debug` for just the debug binary.
build:
	go build -o $(BINARY) $(CMD)

build-debug:
	go build $(DEBUG_GCFLAGS) -o $(BINARY_DEBUG) $(CMD)

# Alias for clarity when only one is wanted.
build-all: build

# debug launches gbot-debug under dlv (interactive REPL).
# gbot runs as a child of dlv, so ptrace_scope=1 is fine.
# Connect from another terminal with: dlv connect :2345
debug: build-debug
	dlv exec ./$(BINARY_DEBUG) --headless --api-version=2 --listen=127.0.0.1:2345

test:
	go test $(PKG) -race -count=1 -timeout 120s -coverprofile=coverage.out
	go test ./test/ -count=1 -timeout 120s
	@echo ""
	@echo "Coverage:"
	@go tool cover -func=coverage.out
	@echo ""
	@echo "Total coverage:"
	@go tool cover -func=coverage.out | tail -1
	@rm -f coverage.out

lint:
	golangci-lint run $(ALL)

check: build test lint fix

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
