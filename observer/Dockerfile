# Multi-stage build for Observer Service
# This single image contains both observer and integrated updater functionality

# ============================================
# Stage 1: Builder
# ============================================
FROM golang:1.24-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /build

# Copy go mod files and download dependencies (layer caching)
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build observer_service binary (includes integrated updater)
# CGO_ENABLED=0 for static binary, ldflags for size reduction
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags='-w -s -extldflags "-static"' \
    -a \
    -o observer_service \
    ./cmd/observer_service

# ============================================
# Stage 2: Runtime
# ============================================
FROM alpine:3.19

# Create non-root user and group with UID/GID 1000
# This ensures files created by the app are owned by 1000:1000
RUN addgroup -g 1000 observer && \
    adduser -D -u 1000 -G observer observer

# Install runtime dependencies
RUN apk add --no-cache ca-certificates tzdata wget

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/observer_service .

# Copy Lua scripts (needed for Redis operations)
COPY --from=builder /build/internal/scripts/*.lua ./internal/scripts/

# Create data/config directories with correct ownership.
# Deployment is expected to mount config files into /app/config.
# This fixes the permission issue: observer user (1000) owns the directory
RUN mkdir -p /app/data /app/config && \
    chown -R observer:observer /app && \
    chmod -R 755 /app/data

# Switch to non-root user BEFORE the app runs
# All files created by the app will have owner 1000:1000
USER observer

# Expose port
EXPOSE 9000

# Health check (if you have /health endpoint)
# HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
#   CMD wget --no-verbose --tries=1 --spider http://localhost:9000/health || exit 1

# Run the observer service
# Data updater runs as integrated background goroutine (UPDATER_ENABLED=true by default)
ENTRYPOINT ["./observer_service"]
