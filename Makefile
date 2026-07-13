.PHONY: all build build-all test test-integration lint vet clean run install release-dry-run check setup-hooks help

# ── Variables ─────────────────────────────────────────────────────
BINARY  := neghab
MODULE  := github.com/sudosz/neghab
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
DATE   ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

GOFLAGS := -ldflags="-s -w -X main.Version=$(VERSION) -X main.BuildDate=$(DATE)"

# ── Default ───────────────────────────────────────────────────────
all: build

# ── Build (current platform) ──────────────────────────────────────
build:
	@printf "\033[36mBuilding %s %s...\033[0m\n" "$(BINARY)" "$(VERSION)"
	go build $(GOFLAGS) -o $(BINARY) .
	@printf "\033[32mDone: ./%s\033[0m\n" "$(BINARY)"

# ── Cross-compile all supported targets ───────────────────────────
build-all: build-linux-amd64 build-linux-arm64

build-linux-amd64:
	@printf "\033[36mBuilding %s for linux/amd64...\033[0m\n" "$(BINARY)"
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build $(GOFLAGS) -o $(BINARY)-linux-amd64 .
	@printf "\033[32mDone: ./%s-linux-amd64\033[0m\n" "$(BINARY)"

build-linux-arm64:
	@printf "\033[36mBuilding %s for linux/arm64...\033[0m\n" "$(BINARY)"
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build $(GOFLAGS) -o $(BINARY)-linux-arm64 .
	@printf "\033[32mDone: ./%s-linux-arm64\033[0m\n" "$(BINARY)"

# ── Test ──────────────────────────────────────────────────────────
test:
	@printf "\033[36mRunning tests...\033[0m\n"
	go test -v -race -count=1 -timeout=60s ./...

# ── Integration tests (requires root + loopback) ───────────────────
test-integration:
	@printf "\033[36mRunning integration tests (loopback + real network)...\033[0m\n"
	go test -v -race -count=1 -timeout=120s -tags=integration ./...

# ── Lint (go vet) ─────────────────────────────────────────────────
lint:
	@printf "\033[36mRunning go vet...\033[0m\n"
	go vet ./...

# Short alias
vet: lint

# ── Clean ─────────────────────────────────────────────────────────
clean:
	@printf "\033[36mCleaning...\033[0m\n"
	rm -f $(BINARY)
	rm -f $(BINARY)-linux-amd64
	rm -f $(BINARY)-linux-arm64
	rm -rf dist/
	@printf "\033[32mDone\033[0m\n"

# ── Run (requires root) ───────────────────────────────────────────
run: build
	@printf "\033[36mStarting %s...\033[0m\n" "$(BINARY)"
	sudo ./$(BINARY) $(ARGS)

# ── Install ───────────────────────────────────────────────────────
install: build
	@printf "\033[36mInstalling %s to /usr/local/bin...\033[0m\n" "$(BINARY)"
	sudo cp $(BINARY) /usr/local/bin/$(BINARY)
	sudo chmod +x /usr/local/bin/$(BINARY)
	@printf "\033[32mDone: /usr/local/bin/%s\033[0m\n" "$(BINARY)"

# ── GoReleaser dry run ────────────────────────────────────────────
release-dry-run:
	goreleaser release --snapshot --clean --skip=publish

# ── Quality gates (all 5 checks) ──────────────────────────────────
check:
	@printf "\033[1;36m═════════════════════════════════════════\033[0m\n"
	@printf "\033[1;36m  Neghab — Quality Gates\033[0m\n"
	@printf "\033[1;36m═════════════════════════════════════════\033[0m\n\n"
	@printf "\033[36m[1/5] go build...\033[0m\n"
	@go build -o /dev/null .
	@printf "\033[32m  ✓ build passed\033[0m\n\n"
	@printf "\033[36m[2/5] go vet...\033[0m\n"
	@go vet ./...
	@printf "\033[32m  ✓ go vet passed\033[0m\n\n"
	@printf "\033[36m[3/5] golangci-lint...\033[0m\n"
	@golangci-lint run ./...
	@printf "\033[32m  ✓ golangci-lint passed\033[0m\n\n"
	@printf "\033[36m[4/5] govulncheck...\033[0m\n"
	@govulncheck ./...
	@printf "\033[32m  ✓ govulncheck passed\033[0m\n\n"
	@printf "\033[36m[5/5] go test (race detector)...\033[0m\n"
	@go test -race -count=1 -timeout=120s ./...
	@printf "\033[32m  ✓ tests passed\033[0m\n\n"
	@printf "\033[1;32m═════════════════════════════════════════\033[0m\n"
	@printf "\033[1;32m  All checks passed ✓\033[0m\n"
	@printf "\033[1;32m═════════════════════════════════════════\033[0m\n"

# ── Install pre-commit hook ───────────────────────────────────────
setup-hooks:
	@printf "\033[36mInstalling pre-commit hook...\033[0m\n"
	@mkdir -p .git/hooks
	@cp .githooks/pre-commit .git/hooks/pre-commit
	@chmod +x .git/hooks/pre-commit
	@printf "\033[32mPre-commit hook installed → .git/hooks/pre-commit\033[0m\n"

# ── Help ──────────────────────────────────────────────────────────
help:
	@printf "  \033[1mNeghab — Build & Release Targets\033[0m\n"
	@printf "\n"
	@printf "  \033[36m%-24s\033[0m %s\n" "make build"          "Build for current platform"
	@printf "  \033[36m%-24s\033[0m %s\n" "make build-all"      "Cross-compile for all platforms"
	@printf "  \033[36m%-24s\033[0m %s\n" "make build-linux-amd64" "Build for linux/amd64"
	@printf "  \033[36m%-24s\033[0m %s\n" "make build-linux-arm64" "Build for linux/arm64"
	@printf "  \033[36m%-24s\033[0m %s\n" "make test"           "Run all tests with race detector"
	@printf "  \033[36m%-24s\033[0m %s\n" "make test-integration" "Run integration tests (needs root)"
	@printf "  \033[36m%-24s\033[0m %s\n" "make lint"           "Run go vet"
	@printf "  \033[36m%-24s\033[0m %s\n" "make clean"          "Remove build artifacts"
	@printf "  \033[36m%-24s\033[0m %s\n" "make run ARGS=..."   "Build and run (e.g., make run ARGS='--interface eth0')"
	@printf "  \033[36m%-24s\033[0m %s\n" "make install"        "Copy binary to /usr/local/bin"
	@printf "  \033[36m%-24s\033[0m %s\n" "make release-dry-run" "Test GoReleaser locally"
	@printf "  \033[36m%-24s\033[0m %s\n" "make check"          "Run all 5 quality gates"
	@printf "  \033[36m%-24s\033[0m %s\n" "make setup-hooks"    "Install git pre-commit hook"
	@printf "\n"
