# syntax=docker/dockerfile:1.4

FROM golang:1.25.1-alpine AS builder

WORKDIR /app
COPY . .

RUN go build -ldflags="-s -w" -o ewelink-api ./cmd/ewelink-api

FROM alpine:latest

RUN apk add --no-cache ca-certificates

WORKDIR /app
COPY --from=builder /app/ewelink-api .

ENTRYPOINT ["/app/ewelink-api"]
