# ==============================================================================
# SUBZERO MULTI-TARGET PRODUCTION DOCKERFILE
# Pure Go runtime, zero CGo, minimal attack surface (PRD 5.2, 8, 9)
# ==============================================================================

# Stage 1: Build Binaries
FROM golang:1.26-alpine AS builder

WORKDIR /src

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source tree
COPY . .

# Compile optimized static binaries
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/subzero-worker ./cmd/subzero-worker && \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/subzero-supervisor ./cmd/subzero-supervisor && \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/subzero-admin ./cmd/subzero-admin && \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/subzero-cli ./cmd/subzero-cli

# ==============================================================================
# Target: worker (Image: subzero-worker:latest used by ContainerSpawner)
# ==============================================================================
FROM alpine:3.19 AS worker
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app

COPY --from=builder /out/subzero-worker /usr/local/bin/subzero-worker

# Default IPC and workspace directories
RUN mkdir -p /tmp /var/subzero && chmod 777 /tmp /var/subzero

ENTRYPOINT ["/usr/local/bin/subzero-worker"]
CMD ["-scheme=tcp", "-endpoint=0.0.0.0:9091", "-idle-timeout=30s"]

# ==============================================================================
# Target: supervisor (Subzero Supervisor Daemon)
# ==============================================================================
FROM alpine:3.19 AS supervisor
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app

COPY --from=builder /out/subzero-supervisor /usr/local/bin/subzero-supervisor
COPY --from=builder /out/subzero-worker /usr/local/bin/subzero-worker

RUN mkdir -p /tmp /var/subzero && chmod 777 /tmp /var/subzero

EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/subzero-supervisor"]
CMD ["-scheme=tcp", "-endpoint=0.0.0.0:9090", "-worker-endpoint=127.0.0.1:9091", "-worker-bin=/usr/local/bin/subzero-worker", "-spawner=process"]

# ==============================================================================
# Target: admin (Management API & Web Dashboard)
# ==============================================================================
FROM golang:1.26-alpine AS admin
RUN apk --no-cache add ca-certificates tzdata git
WORKDIR /app

COPY --from=builder /out/subzero-admin /usr/local/bin/subzero-admin
COPY --from=builder /out/subzero-cli /usr/local/bin/subzero-cli

RUN mkdir -p /app/data/wasm && chmod 777 /app/data/wasm

EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/subzero-admin"]
CMD ["-port=8080", "-data-dir=/app/data/wasm"]
