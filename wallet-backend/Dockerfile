# Build stage
FROM golang:1.26-alpine AS builder

WORKDIR /app

# Copy go.mod and go.sum first for cached dependency download.
COPY go.mod ./
COPY go.sum* ./
RUN go mod download

# Copy the rest of the source code and build.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o wallet-backend .

# Run stage
FROM alpine:latest

WORKDIR /app

# ca-certificates: wallet-backend makes outbound HTTPS calls (Solana RPC, Jupiter).
RUN apk --no-cache add ca-certificates

COPY --from=builder /app/wallet-backend .

CMD ["./wallet-backend"]
