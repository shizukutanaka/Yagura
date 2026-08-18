# Yagura development Makefile.
#
# Common workflows:
#   make            — fmt + vet + test
#   make test       — full test with race detector
#   make cover      — coverage report
#   make lint       — golangci-lint
#   make build      — build static binary into bin/yagura
#   make ci         — run everything CI runs
#   make fuzz       — run all fuzz tests for 30 seconds each
#   make verify     — build + verify reproducibility
#
# Variables:
#   VERSION         — version string for ldflags injection (default: dev)
#   GOOS / GOARCH   — for cross-compilation
#
# Conventions:
#   - All commands use the Go standard library tooling
#   - Output binaries go to bin/, not the repo root
#   - bin/ and coverage.out are gitignored

SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DELETE_ON_ERROR:
.SUFFIXES:

VERSION ?= dev
GOFLAGS := -trimpath -buildvcs=false
LDFLAGS := -s -w -X main.version=$(VERSION)
BIN_DIR := bin

# Default target: quick local sanity check.
.PHONY: all
all: fmt vet test

# ─── basic dev loop ──────────────────────────────────────────

.PHONY: fmt
fmt:
	gofmt -s -w .

.PHONY: fmt-check
fmt-check:
	@out=$$(gofmt -s -l . 2>&1); \
	if [ -n "$$out" ]; then echo "gofmt issues:"; echo "$$out"; exit 1; fi

.PHONY: vet
vet:
	go vet ./...

.PHONY: test
test:
	go test -race -count=1 -timeout 5m ./...

.PHONY: test-short
test-short:
	go test -short -race -count=1 ./...

.PHONY: cover
cover:
	go test -race -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -func=coverage.out | tail -1

.PHONY: cover-html
cover-html: cover
	go tool cover -html=coverage.out -o coverage.html
	@echo "coverage report: file://$(PWD)/coverage.html"

.PHONY: lint
lint:
	golangci-lint run --timeout=5m ./...

.PHONY: vuln
vuln:
	@command -v govulncheck >/dev/null 2>&1 || go install golang.org/x/vuln/cmd/govulncheck@latest
	govulncheck ./...

# ─── fuzz ───────────────────────────────────────────────────

.PHONY: fuzz
fuzz:
	go test -fuzz=FuzzAppend -fuzztime=30s -run=^$$ ./internal/audit/
	go test -fuzz=FuzzVerify -fuzztime=30s -run=^$$ ./internal/audit/
	go test -fuzz=FuzzAdd    -fuzztime=30s -run=^$$ ./internal/registry/

# ─── build ──────────────────────────────────────────────────

.PHONY: build
build:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/yagura ./cmd/yagura
	@ls -la $(BIN_DIR)/yagura

.PHONY: build-all
build-all:
	@mkdir -p $(BIN_DIR)
	@for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do \
		os=$${target%/*}; arch=$${target#*/}; \
		ext=""; if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		echo "Building $$os/$$arch..."; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build $(GOFLAGS) -ldflags="$(LDFLAGS)" \
			-o $(BIN_DIR)/yagura-$$os-$$arch$$ext ./cmd/yagura; \
	done
	@ls -la $(BIN_DIR)/

# ─── reproducibility ────────────────────────────────────────

.PHONY: verify
verify: build
	@echo "→ Building a second time to check reproducibility..."
	@mkdir -p $(BIN_DIR)/verify
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/verify/yagura ./cmd/yagura
	@sum1=$$(sha256sum $(BIN_DIR)/yagura | awk '{print $$1}'); \
	 sum2=$$(sha256sum $(BIN_DIR)/verify/yagura | awk '{print $$1}'); \
	 echo "first  build: $$sum1"; \
	 echo "second build: $$sum2"; \
	 if [ "$$sum1" = "$$sum2" ]; then \
	   echo "✓ reproducible: byte-for-byte identical"; \
	 else \
	   echo "✗ NOT reproducible: hashes differ"; \
	   exit 1; \
	 fi

# ─── CI parity ──────────────────────────────────────────────

.PHONY: ci
ci: fmt-check vet lint test cover vuln

# ─── housekeeping ───────────────────────────────────────────

.PHONY: clean
clean:
	rm -rf $(BIN_DIR) coverage.out coverage.html

.PHONY: tidy
tidy:
	go mod tidy
	@test ! -s go.sum || (echo "go.sum should be empty per ADR-0001" && exit 1)

# ─── documentation ────────────────────────────────────

.PHONY: docs-mcp
docs-mcp: build
	@echo "→ Regenerating docs/MCP_TOOLS.md from a live yagura..."
	@bash scripts/gen-mcp-docs.sh


.PHONY: release
release:
	@test -n "$(VERSION)" || { echo "usage: make release VERSION=x.y.z"; exit 2; }
	@bash scripts/release.sh "$(VERSION)"

.PHONY: tag
tag:
	@test -n "$(VERSION)" || { echo "usage: make tag VERSION=x.y.z"; exit 2; }
	@bash scripts/tag.sh "$(VERSION)"
