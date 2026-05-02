# Build stage
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-X github.com/openagentsinc/bahia/internal/api/router.Version=${VERSION}" \
    -o /bin/bahia-server ./cmd/server

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-X github.com/openagentsinc/bahia/internal/api/router.Version=${VERSION}" \
    -o /bin/bahia ./cmd/cli

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-X github.com/openagentsinc/bahia/internal/api/router.Version=${VERSION}" \
    -o /bin/bahia-relay ./cmd/relay

# Runtime stage
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata docker-cli docker-cli-compose wget

COPY --from=builder /bin/bahia-server /usr/local/bin/bahia-server
COPY --from=builder /bin/bahia /usr/local/bin/bahia
COPY --from=builder /bin/bahia-relay /usr/local/bin/bahia-relay

EXPOSE 8080 3334

ENTRYPOINT ["bahia-server"]
