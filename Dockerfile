# syntax=docker/dockerfile:1
# Build stage
# golang:1.26.3-alpine
FROM golang:1.26.3-alpine@sha256:91eda9776261207ea25fd06b5b7fed8d397dd2c0a283e77f2ab6e91bfa71079d AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /src

ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}

COPY go.mod go.sum ./
RUN go mod download

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
    -o /bin/fips-bahia-bridge ./cmd/fips-bahia-bridge

RUN VERSION_VALUE="${VERSION:-${VERSION_BASE}-${GIT_COMMIT}}" && \
    CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-X github.com/openagentsinc/bahia/internal/version.Base=${VERSION_BASE} -X github.com/openagentsinc/bahia/internal/version.Commit=${GIT_COMMIT} -X github.com/openagentsinc/bahia/internal/version.Full=${VERSION_VALUE}" \
    -o /bin/openclaw-soulfactory-sidecar ./cmd/openclaw-soulfactory-sidecar

RUN VERSION_VALUE="${VERSION:-${VERSION_BASE}-${GIT_COMMIT}}" && \
    CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-X github.com/openagentsinc/bahia/internal/version.Base=${VERSION_BASE} -X github.com/openagentsinc/bahia/internal/version.Commit=${GIT_COMMIT} -X github.com/openagentsinc/bahia/internal/version.Full=${VERSION_VALUE}" \
    -o /bin/openclaw-soulfactory-control ./cmd/openclaw-soulfactory-control

RUN VERSION_VALUE="${VERSION:-${VERSION_BASE}-${GIT_COMMIT}}" && \
    CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-X github.com/openagentsinc/bahia/internal/version.Base=${VERSION_BASE} -X github.com/openagentsinc/bahia/internal/version.Commit=${GIT_COMMIT} -X github.com/openagentsinc/bahia/internal/version.Full=${VERSION_VALUE}" \
    -o /bin/bahia-event-archive ./cmd/bahia-event-archive

RUN VERSION_VALUE="${VERSION:-${VERSION_BASE}-${GIT_COMMIT}}" && \
    CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-X github.com/openagentsinc/bahia/internal/version.Base=${VERSION_BASE} -X github.com/openagentsinc/bahia/internal/version.Commit=${GIT_COMMIT} -X github.com/openagentsinc/bahia/internal/version.Full=${VERSION_VALUE}" \
    -o /bin/metiq-signet-enrollment ./cmd/metiq-signet-enrollment

RUN VERSION_VALUE="${VERSION:-${VERSION_BASE}-${GIT_COMMIT}}" && \
    CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-X github.com/openagentsinc/bahia/internal/version.Base=${VERSION_BASE} -X github.com/openagentsinc/bahia/internal/version.Commit=${GIT_COMMIT} -X github.com/openagentsinc/bahia/internal/version.Full=${VERSION_VALUE}" \
    -o /bin/soulfactory-runtime-validate ./cmd/soulfactory-runtime-validate

RUN VERSION_VALUE="${VERSION:-${VERSION_BASE}-${GIT_COMMIT}}" && \
    CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-X github.com/openagentsinc/bahia/internal/version.Base=${VERSION_BASE} -X github.com/openagentsinc/bahia/internal/version.Commit=${GIT_COMMIT} -X github.com/openagentsinc/bahia/internal/version.Full=${VERSION_VALUE}" \
    -o /bin/bahia-relay ./cmd/relay

# Runtime stage
# alpine:3.21
FROM alpine:3.21@sha256:ce64758a109eb420d874a118f87920e625e12d3634e03b4a5573fd9f6e5d3507

ARG GIT_COMMIT=dev
ARG BUILD_DATE=unknown
ARG RELAY_FLOOD_GUARD=2026-09-15-v1

LABEL org.opencontainers.image.source="https://github.com/openagentsinc/bahia" \
      org.opencontainers.image.revision="${GIT_COMMIT}" \
      org.opencontainers.image.created="${BUILD_DATE}" \
      io.cascadia.bahia.relay-flood-guard="${RELAY_FLOOD_GUARD}"

ARG VERSION_BASE=0.1.0
ARG GIT_COMMIT=dev
ARG VERSION=0.1.0-dev
LABEL org.opencontainers.image.source="https://github.com/openagentsinc/bahia" \
      org.opencontainers.image.revision="${GIT_COMMIT}" \
      org.opencontainers.image.version="${VERSION}"

RUN apk add --no-cache ca-certificates tzdata wget && \
    addgroup -S docker && \
    adduser -S -G docker bahia

USER bahia

COPY --from=builder /bin/bahia-server /usr/local/bin/bahia-server
COPY --from=builder /bin/bahia /usr/local/bin/bahia
COPY --from=builder /bin/bahia-relay /usr/local/bin/bahia-relay
COPY --from=builder /bin/fips-bahia-bridge /usr/local/bin/fips-bahia-bridge
COPY --from=builder /bin/openclaw-soulfactory-sidecar /usr/local/bin/openclaw-soulfactory-sidecar
COPY --from=builder /bin/openclaw-soulfactory-control /usr/local/bin/openclaw-soulfactory-control
COPY --from=builder /bin/bahia-event-archive /usr/local/bin/bahia-event-archive
COPY --from=builder /bin/metiq-signet-enrollment /usr/local/bin/metiq-signet-enrollment
COPY --from=builder /bin/soulfactory-runtime-validate /usr/local/bin/soulfactory-runtime-validate

EXPOSE 8080 3334

ENTRYPOINT ["bahia-server"]
