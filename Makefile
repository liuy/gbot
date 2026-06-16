.PHONY: all build test lint check clean agent-start agent-stop install

BINARY := gbot
CMD := ./cmd/gbot/
PKG := ./pkg/...
ALL := ./pkg/... ./cmd/...
GBOT_HOME := $(HOME)/.gbot

all: build
	./$(BINARY)

build:
	go build -o $(BINARY) $(CMD)

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
	rm -f $(BINARY) coverage.out *.out *.prof *.test
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
