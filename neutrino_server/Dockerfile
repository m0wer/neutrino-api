# Build stage
FROM golang:1.27.0-alpine3.23 AS builder

ARG VERSION=dev

WORKDIR /build

# Copy go.mod and go.sum for dependency caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o neutrinod ./cmd/neutrinod

# Runtime stage
FROM alpine:3.23.5
RUN apk add --no-cache ca-certificates tzdata

# Create non-root user
RUN adduser -D -u 1000 neutrino

# Create data directory
RUN mkdir -p /data/neutrino && chown neutrino:neutrino /data/neutrino

# Copy binary from builder
COPY --from=builder /build/neutrinod /usr/local/bin/

USER neutrino
WORKDIR /data

# Default configuration
ENV NETWORK=mainnet
ENV LISTEN_ADDR=0.0.0.0:8334
ENV DATA_DIR=/data/neutrino
ENV LOG_LEVEL=info
ENV MAX_PEERS=8

EXPOSE 8334

VOLUME ["/data/neutrino"]

ENTRYPOINT ["/bin/sh", "-c"]
CMD ["exec neutrinod -network=$NETWORK -listen=$LISTEN_ADDR -datadir=$DATA_DIR -loglevel=$LOG_LEVEL -addpeer=$ADD_PEERS"]
