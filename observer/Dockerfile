FROM golang:1.24.4-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o observer_service ./cmd/observer_service

FROM alpine:3.22

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

# Create directories for data and configs with proper permissions
RUN mkdir -p /app/data /app/config && \
    chmod 777 /app/data && \
    chmod 755 /app/config

COPY --from=builder /app/observer_service .

COPY --from=builder /app/internal/scripts/add_and_check_ip.lua ./internal/scripts/add_and_check_ip.lua
COPY --from=builder /app/internal/scripts/add_and_check_subnet.lua ./internal/scripts/add_and_check_subnet.lua
COPY --from=builder /app/internal/scripts/add_and_check_asn.lua ./internal/scripts/add_and_check_asn.lua

CMD ["/app/observer_service"]