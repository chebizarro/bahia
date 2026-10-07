.PHONY: build run test race lint lint-arch arch-baseline clean migrate docker docker-compose soulfactory-coverage build-server build-cli build-relay build-fips-bahia-bridge build-openclaw-soulfactory-sidecar build-openclaw-soulfactory-control build-bahia-event-archive build-metiq-signet-enrollment build-soulfactory-runtime-validate build-bahia-dns-agent build-bahia-migrate dist-bahia-dns-agent dist-bahia-dns-agent-linux-amd64 dist-bahia-dns-agent-linux-arm64 dist-bahia-dns-agent-linux-mips-softfloat

VERSION_BASE ?= 0.1.0
GIT_COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null || echo "dev")
VERSION ?= $(VERSION_BASE)-$(GIT_COMMIT)
LDFLAGS := -ldflags "-X github.com/openagentsinc/bahia/internal/version.Base=$(VERSION_BASE) -X github.com/openagentsinc/bahia/internal/version.Commit=$(GIT_COMMIT) -X github.com/openagentsinc/bahia/internal/version.Full=$(VERSION)"

# Build
build: build-server build-cli build-relay build-fips-bahia-bridge build-openclaw-soulfactory-sidecar build-openclaw-soulfactory-control build-bahia-event-archive build-metiq-signet-enrollment build-soulfactory-runtime-validate build-bahia-dns-agent build-bahia-migrate

build-server:
	go build $(LDFLAGS) -o bin/bahia-server ./cmd/server

build-cli:
	go build $(LDFLAGS) -o bin/bahia ./cmd/cli

build-relay:
	go build $(LDFLAGS) -o bin/bahia-relay ./cmd/relay

build-fips-bahia-bridge:
	go build $(LDFLAGS) -o bin/fips-bahia-bridge ./cmd/fips-bahia-bridge

build-openclaw-soulfactory-sidecar:
	go build $(LDFLAGS) -o bin/openclaw-soulfactory-sidecar ./cmd/openclaw-soulfactory-sidecar

build-openclaw-soulfactory-control:
	go build $(LDFLAGS) -o bin/openclaw-soulfactory-control ./cmd/openclaw-soulfactory-control

build-bahia-migrate:
	go build $(LDFLAGS) -o bin/bahia-migrate ./cmd/bahia-migrate

build-bahia-event-archive:
	go build $(LDFLAGS) -o bin/bahia-event-archive ./cmd/bahia-event-archive

build-metiq-signet-enrollment:
	go build $(LDFLAGS) -o bin/metiq-signet-enrollment ./cmd/metiq-signet-enrollment

build-soulfactory-runtime-validate:
	go build $(LDFLAGS) -o bin/soulfactory-runtime-validate ./cmd/soulfactory-runtime-validate

build-bahia-dns-agent:
	CGO_ENABLED=0 go build $(LDFLAGS) -o bin/bahia-dns-agent ./cmd/bahia-dns-agent

# Portable static cross-compiles for LAN resolver hosts (see cmd/bahia-dns-agent/README.md).
# Builds linux-amd64 and linux-arm64. linux-mips-softfloat is deliberately excluded
# because it always fails today (see bahia-1m1ef); build it explicitly via
# dist-bahia-dns-agent-linux-mips-softfloat once that issue is resolved.
dist-bahia-dns-agent: dist-bahia-dns-agent-linux-amd64 dist-bahia-dns-agent-linux-arm64

dist-bahia-dns-agent-linux-amd64:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o bin/bahia-dns-agent-linux-amd64 ./cmd/bahia-dns-agent

dist-bahia-dns-agent-linux-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o bin/bahia-dns-agent-linux-arm64 ./cmd/bahia-dns-agent

# Known limitation: currently fails because internal/controlplane transitively
# links modernc.org/sqlite (via internal/adapters/nostr -> internal/adapters/sbom),
# whose libc has no 32-bit MIPS port. Tracked in beads as bahia-1m1ef; excluded
# from the dist-bahia-dns-agent aggregate until fixed.
dist-bahia-dns-agent-linux-mips-softfloat:
	CGO_ENABLED=0 GOOS=linux GOARCH=mips GOMIPS=softfloat go build $(LDFLAGS) -o bin/bahia-dns-agent-linux-mips-softfloat ./cmd/bahia-dns-agent

# Run
run: build-server
	./bin/bahia-server

run-dev:
	go run $(LDFLAGS) ./cmd/server -config config.yaml

# Test
test:
	go test ./... -v -count=1

race:
	CGO_ENABLED=1 go test -race ./... -count=1

test-short:
	go test ./... -short -count=1

test-coverage:
	go test ./... -coverprofile=coverage.out -count=1
	go tool cover -html=coverage.out -o coverage.html

# Soul Factory coverage (Go + web) into the gitignored coverage/ directory.
soulfactory-coverage:
	mkdir -p coverage/soulfactory
	go test ./internal/soulfactory ./cmd/cli -coverprofile=coverage/soulfactory/go_coverage.out -count=1
	go tool cover -func=coverage/soulfactory/go_coverage.out > coverage/soulfactory/go_coverage_summary.txt
	cd web && npm run test:unit:coverage:soulfactory

# Lint
lint: lint-arch
	golangci-lint run ./...

# Architecture ratchet gates (bahia-irsry.8). Each gate compares against a
# checked-in baseline of pre-existing violations and fails only on new ones:
# legacy kinds outside internal/nostrmigration, direct library relay
# subscriptions outside the pool/bus, unannotated poll tickers in
# internal/service and internal/reconcile, test-only exported symbols in
# internal/, the DB-less boot invariants, and web store setInterval /
# $$lib/api/client.js imports. They also run under `go test ./...` and
# `pnpm run test:unit`.
ARCH_GO_GATES = go test ./internal/archtest ./internal/app -run 'TestNoNew|TestArchitecture' -count=1
ARCH_WEB_GATES = pnpm exec vitest run --config vitest.config.js tests/unit/architecture-gates.test.js
lint-arch:
	CGO_ENABLED=0 $(ARCH_GO_GATES)
	cd web && $(ARCH_WEB_GATES)

# Regenerate every architecture baseline from the current tree. Run after
# violations are removed (baselines only shrink) or on an integration branch.
# Prints a "BASELINE SUMMARY" per gate: "+" lines are new or grown debt and
# need a stated reason; "-" lines were paid down. Review with git diff.
arch-baseline:
	CGO_ENABLED=0 ARCHTEST_UPDATE_BASELINE=1 go test ./internal/archtest -count=1 -v -run 'TestNoNew'
	cd web && ARCHTEST_UPDATE_BASELINE=1 $(ARCH_WEB_GATES)
	git diff --stat -- internal/archtest/testdata web/tests/unit/architecture-gates.baseline.json

# Format. third_party/ holds a vendored upstream module (a separate Go module,
# so ./... targets already skip it); keep formatters from rewriting it too.
GO_FMT_FILES = $$(find . -name '*.go' -not -path './third_party/*' -not -path './.git/*' -not -path './web/node_modules/*')
fmt:
	gofmt -w $(GO_FMT_FILES)
	goimports -w $(GO_FMT_FILES)

# Clean
clean:
	rm -rf bin/ coverage.out coverage.html

# Database
MIGRATE_CONFIG ?= config.yaml
MIGRATE_ACTION ?= up
MIGRATE_FLAGS ?=
migrate:
	go run ./cmd/bahia-migrate --config $(MIGRATE_CONFIG) $(MIGRATE_FLAGS) $(MIGRATE_ACTION)

# Docker
docker:
	docker build --build-arg VERSION_BASE=$(VERSION_BASE) --build-arg GIT_COMMIT=$(GIT_COMMIT) --build-arg VERSION=$(VERSION) -t bahia:$(VERSION) .

docker-compose:
	docker compose up --build

docker-compose-down:
	docker compose down -v

# Dependencies
deps:
	go mod download
	go mod tidy

# Generate (for future sqlc or other codegen)
generate:
	go generate ./...
