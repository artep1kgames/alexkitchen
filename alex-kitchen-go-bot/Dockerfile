# Build stage
FROM golang:1.24-bookworm AS builder

WORKDIR /build

COPY go.mod go.sum ./
COPY max-bot-api-client-go/ ./max-bot-api-client-go/

RUN go mod download

COPY main.go ./

RUN CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags="-s -w" \
    -o /bot main.go

# Runtime stage
FROM debian:bookworm-slim

WORKDIR /app

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/*

COPY russian_trusted_root_ca.pem /usr/local/share/ca-certificates/russian_trusted_root_ca.crt

RUN update-ca-certificates

COPY --from=builder /bot ./bot

ENV TZ=Europe/Moscow

CMD ["./bot"]
