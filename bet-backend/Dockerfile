# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Copy go.mod and go.sum (which will be created)
COPY go.mod ./
# If go.sum exists, it will copy it, otherwise wildcard avoids failure if no go.sum yet
COPY go.sum* ./

RUN go mod download

# Copy the rest of the source code
COPY . .

# Build the Go application
RUN CGO_ENABLED=0 GOOS=linux go build -o bet-backend .

# Run stage
FROM alpine:latest

WORKDIR /app

# Install ca-certificates in case we make external HTTPS requests
RUN apk --no-cache add ca-certificates

COPY --from=builder /app/bet-backend .

EXPOSE 8080

CMD ["./bet-backend"]
