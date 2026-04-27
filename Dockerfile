# Build stage
FROM golang:1.24-alpine AS builder

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

# Runtime stage
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /bin/bahia-server /usr/local/bin/bahia-server
COPY --from=builder /bin/bahia /usr/local/bin/bahia

EXPOSE 8080

ENTRYPOINT ["bahia-server"]
