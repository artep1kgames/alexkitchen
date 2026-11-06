# Build stage
FROM golang:1.26-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/comments-app .

# Runtime stage
FROM debian:bookworm-slim

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata wget && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/comments-app ./comments-app
COPY static/ ./static/

ENV TZ=Europe/Moscow

EXPOSE 8081

CMD ["./comments-app"]
