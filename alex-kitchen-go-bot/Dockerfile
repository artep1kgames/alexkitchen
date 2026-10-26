
FROM golang:1.25-alpine AS builder

WORKDIR /build

COPY go.mod go.sum ./
COPY max-bot-api-client-go/ ./max-bot-api-client-go/

RUN go mod download

COPY main.go ./

RUN CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags="-s -w" \
    -o /bot main.go

FROM alpine:3.22

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

RUN apt update
RUN apt install --reinstall ca-certificates -y
RUN update-ca-certificates

COPY --from=builder /bot ./bot

ENV TZ=Europe/Moscow

CMD ["./bot"]
