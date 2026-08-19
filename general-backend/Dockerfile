# Build stage
FROM golang:1.26-alpine AS builder

WORKDIR /app

RUN apk add --no-cache curl libstdc++ libgcc

# templ CLI (turns *.templ into *_templ.go before go build) and the Tailwind
# standalone CLI (single binary, no Node/npm anywhere in this repo).
RUN go install github.com/a-h/templ/cmd/templ@v0.3.1020
RUN curl --fail --location --http1.1 --continue-at - --retry 5 --retry-all-errors --retry-delay 2 \
	-o /usr/local/bin/tailwindcss https://github.com/tailwindlabs/tailwindcss/releases/download/v4.3.2/tailwindcss-linux-x64-musl \
	&& chmod +x /usr/local/bin/tailwindcss

# Copy go.mod and go.sum (which will be created)
COPY go.mod ./
# If go.sum exists, it will copy it, otherwise wildcard avoids failure if no go.sum yet
COPY go.sum* ./

RUN go mod download

# Copy the rest of the source code
COPY . .

# templ generate must run before go build - it produces the _templ.go files
# the compiler needs. Tailwind scans the generated Go files (the HTML classes
# live in Go string literals after templ generate) for class usage.
RUN templ generate
RUN tailwindcss -i ./static/css/input.css -o ./static/css/tailwind.css --minify

# Build the Go application
RUN CGO_ENABLED=0 GOOS=linux go build -o general-backend .

# Run stage
FROM alpine:latest

WORKDIR /app

# Install ca-certificates in case we make external HTTPS requests
RUN apk --no-cache add ca-certificates

COPY --from=builder /app/general-backend .
COPY --from=builder /app/static ./static

EXPOSE 8081

CMD ["./general-backend"]
