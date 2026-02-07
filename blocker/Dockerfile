FROM golang:1.24.4-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o blocker-worker ./cmd/blocker_worker

FROM debian:bookworm-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends nftables ca-certificates && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=builder /app/blocker-worker .

CMD ["/app/blocker-worker"]