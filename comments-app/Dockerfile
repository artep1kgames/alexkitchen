
# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /build

COPY main.go .

RUN CGO_ENABLED=0 GO111MODULE=off \
    go build -trimpath -ldflags="-s -w" -o comments-app main.go

# Runtime stage
FROM alpine:3.22

WORKDIR /app

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /build/comments-app ./comments-app

COPY static/ ./static/
COPY comments.json products.json ./
COPY pending-orders.jsonl confirmed-orders.txt ./

ENV TZ=Europe/Moscow

EXPOSE 8081

CMD ["./comments-app"]
