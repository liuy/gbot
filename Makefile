.PHONY: all build test lint clean agent-start agent-stop

BINARY := gbot
CMD := ./cmd/gbot/
PKG := ./pkg/...
ALL := ./pkg/... ./cmd/...

all: build
	./$(BINARY)

build:
	go build -o $(BINARY) $(CMD)

test:
	go test $(PKG) -count=1 -timeout 60s -coverprofile=coverage.out
	go tool cover -func=coverage.out
	@echo ""
	@echo "Total coverage:"
	@go tool cover -func=coverage.out | tail -1
	@rm -f coverage.out

lint:
	golangci-lint run $(ALL)

clean:
	rm -f $(BINARY) coverage.out *.out *.prof *.test
	rm -f /tmp/gbot-screen.raw /tmp/gbot-agent.pid /tmp/gbot-input
	screen -S gbot -X quit 2>/dev/null || true
	go clean
	@echo "cleaned"

# e2e
agent-start: build
	./gbot-agent start --no-build

agent-stop:
	./gbot-agent stop
