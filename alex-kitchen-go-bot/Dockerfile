
# Build stage — Debian
FROM golang:1.25-bookworm AS builder

WORKDIR /build

# Зависимости Go
COPY go.mod go.sum ./
COPY max-bot-api-client-go/ ./max-bot-api-client-go/

RUN go mod download

# Исходный код
COPY main.go ./

# Компиляция
RUN CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags="-s -w" \
    -o /bot main.go

# Runtime stage — Debian
FROM debian:bookworm-slim

WORKDIR /app

# Сертификаты и часовые пояса
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        tzdata && \
    rm -rf /var/lib/apt/lists/*

RUN apt update
RUN apt install --reinstall ca-certificates -y
RUN update-ca-certificates
RUN apt update
RUN apt install --reinstall ca-certificates -y
RUN update-ca-certificates

# Добавляем сертификат Минцифры
COPY russian_trusted_root_ca.pem /usr/local/share/ca-certificates/russian_trusted_root_ca.crt

# Обновляем хранилище сертификатов
RUN update-ca-certificates

COPY --from=builder /bot ./bot

ENV TZ=Europe/Moscow

CMD ["./bot"]

