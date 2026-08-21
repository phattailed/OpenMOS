# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /build

# Copy go.mod and go.sum first for layer caching
COPY src/go.mod src/go.sum ./

RUN go mod download

# Copy full source
COPY src/ ./

# Build statically linked binary
RUN CGO_ENABLED=0 GOOS=linux go build -o openmos .

# Runtime stage
FROM alpine:3.20

WORKDIR /app

# Install ca-certificates for TLS (MongoDB Atlas, Sentry)
RUN apk --no-cache add ca-certificates

# Copy binary from build stage
COPY --from=builder /build/openmos .

EXPOSE 10540

ENTRYPOINT ["./openmos"]
