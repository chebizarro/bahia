.PHONY: build run test lint clean migrate docker docker-compose pstf-soulfactory-coverage

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X github.com/openagentsinc/bahia/internal/api/router.Version=$(VERSION)"

# Build
build: build-server build-cli

build-server:
	go build $(LDFLAGS) -o bin/bahia-server ./cmd/server

build-cli:
	go build $(LDFLAGS) -o bin/bahia ./cmd/cli

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
	docker build -t bahia:$(VERSION) .

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
