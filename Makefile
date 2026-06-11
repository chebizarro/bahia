.PHONY: build run test lint clean migrate docker docker-compose pstf-soulfactory-coverage build-server build-cli build-relay build-fips-bahia-bridge build-openclaw-soulfactory-sidecar build-openclaw-soulfactory-control

VERSION_BASE ?= 0.1.0
GIT_COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null || echo "dev")
VERSION ?= $(VERSION_BASE)-$(GIT_COMMIT)
LDFLAGS := -ldflags "-X github.com/openagentsinc/bahia/internal/version.Base=$(VERSION_BASE) -X github.com/openagentsinc/bahia/internal/version.Commit=$(GIT_COMMIT) -X github.com/openagentsinc/bahia/internal/version.Full=$(VERSION)"

# Build
build: build-server build-cli build-relay build-fips-bahia-bridge build-openclaw-soulfactory-sidecar build-openclaw-soulfactory-control

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

# Run
run: build-server
	./bin/bahia-server

run-dev:
	go run $(LDFLAGS) ./cmd/server -config config.yaml

# Test
test:
	go test ./... -v -count=1

test-short:
	go test ./... -short -count=1

test-coverage:
	go test ./... -coverprofile=coverage.out -count=1
	go tool cover -html=coverage.out -o coverage.html

pstf-soulfactory-coverage:
	mkdir -p pstf/features/SOUL_FACTORY_PROVISIONING_TRACKING/coverage
	go test ./internal/soulfactory ./cmd/cli -coverprofile=pstf/features/SOUL_FACTORY_PROVISIONING_TRACKING/coverage/go_coverage.out -count=1
	go tool cover -func=pstf/features/SOUL_FACTORY_PROVISIONING_TRACKING/coverage/go_coverage.out > pstf/features/SOUL_FACTORY_PROVISIONING_TRACKING/coverage/go_coverage_summary.txt
	cd web && npm run test:unit:coverage:soulfactory

# Lint
lint:
	golangci-lint run ./...

# Format
fmt:
	gofmt -w .
	goimports -w .

# Clean
clean:
	rm -rf bin/ coverage.out coverage.html

# Database
migrate:
	go run ./cmd/server -config config.yaml

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
