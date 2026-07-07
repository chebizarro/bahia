# syntax=docker/dockerfile:1
# Build stage
FROM golang:1.26.3-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /src

# Private-module resolution for git.sharegap.net (cascadia-go / cascadia-nips).
# GOPRIVATE skips the public proxy + checksum DB for the fleet prefix; the git-auth
# secret (a .netrc mounted via BuildKit, not baked into any layer) authenticates the
# private Gitea. Build with:
#   DOCKER_BUILDKIT=1 docker build --secret id=gitauth,src=$HOME/.netrc .
# where ~/.netrc contains:  machine git.sharegap.net login <user> password <token>
ENV GOPRIVATE=git.sharegap.net/*

COPY go.mod go.sum ./
RUN --mount=type=secret,id=gitauth,target=/root/.netrc \
    go mod download

COPY . .

ARG VERSION_BASE=0.1.0
ARG GIT_COMMIT=dev
ARG VERSION=
RUN VERSION_VALUE="${VERSION:-${VERSION_BASE}-${GIT_COMMIT}}" && \
    CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-X github.com/openagentsinc/bahia/internal/version.Base=${VERSION_BASE} -X github.com/openagentsinc/bahia/internal/version.Commit=${GIT_COMMIT} -X github.com/openagentsinc/bahia/internal/version.Full=${VERSION_VALUE}" \
    -o /bin/bahia-server ./cmd/server

RUN VERSION_VALUE="${VERSION:-${VERSION_BASE}-${GIT_COMMIT}}" && \
    CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-X github.com/openagentsinc/bahia/internal/version.Base=${VERSION_BASE} -X github.com/openagentsinc/bahia/internal/version.Commit=${GIT_COMMIT} -X github.com/openagentsinc/bahia/internal/version.Full=${VERSION_VALUE}" \
    -o /bin/bahia ./cmd/cli

RUN VERSION_VALUE="${VERSION:-${VERSION_BASE}-${GIT_COMMIT}}" && \
    CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-X github.com/openagentsinc/bahia/internal/version.Base=${VERSION_BASE} -X github.com/openagentsinc/bahia/internal/version.Commit=${GIT_COMMIT} -X github.com/openagentsinc/bahia/internal/version.Full=${VERSION_VALUE}" \
    -o /bin/bahia-relay ./cmd/relay

RUN VERSION_VALUE="${VERSION:-${VERSION_BASE}-${GIT_COMMIT}}" && \
    CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-X github.com/openagentsinc/bahia/internal/version.Base=${VERSION_BASE} -X github.com/openagentsinc/bahia/internal/version.Commit=${GIT_COMMIT} -X github.com/openagentsinc/bahia/internal/version.Full=${VERSION_VALUE}" \
    -o /bin/fips-bahia-bridge ./cmd/fips-bahia-bridge

RUN VERSION_VALUE="${VERSION:-${VERSION_BASE}-${GIT_COMMIT}}" && \
    CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-X github.com/openagentsinc/bahia/internal/version.Base=${VERSION_BASE} -X github.com/openagentsinc/bahia/internal/version.Commit=${GIT_COMMIT} -X github.com/openagentsinc/bahia/internal/version.Full=${VERSION_VALUE}" \
    -o /bin/openclaw-soulfactory-sidecar ./cmd/openclaw-soulfactory-sidecar

RUN VERSION_VALUE="${VERSION:-${VERSION_BASE}-${GIT_COMMIT}}" && \
    CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-X github.com/openagentsinc/bahia/internal/version.Base=${VERSION_BASE} -X github.com/openagentsinc/bahia/internal/version.Commit=${GIT_COMMIT} -X github.com/openagentsinc/bahia/internal/version.Full=${VERSION_VALUE}" \
    -o /bin/openclaw-soulfactory-control ./cmd/openclaw-soulfactory-control

# Runtime stage
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata docker-cli docker-cli-compose wget

COPY --from=builder /bin/bahia-server /usr/local/bin/bahia-server
COPY --from=builder /bin/bahia /usr/local/bin/bahia
COPY --from=builder /bin/bahia-relay /usr/local/bin/bahia-relay
COPY --from=builder /bin/fips-bahia-bridge /usr/local/bin/fips-bahia-bridge
COPY --from=builder /bin/openclaw-soulfactory-sidecar /usr/local/bin/openclaw-soulfactory-sidecar
COPY --from=builder /bin/openclaw-soulfactory-control /usr/local/bin/openclaw-soulfactory-control

EXPOSE 8080 3334

ENTRYPOINT ["bahia-server"]
