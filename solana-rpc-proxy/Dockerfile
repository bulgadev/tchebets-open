FROM golang:1.26-alpine AS builder

WORKDIR /app
COPY go.mod ./
COPY main.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -o solana-rpc-proxy .

FROM alpine:latest

RUN apk --no-cache add ca-certificates
COPY --from=builder /app/solana-rpc-proxy /usr/local/bin/solana-rpc-proxy
CMD ["solana-rpc-proxy"]
