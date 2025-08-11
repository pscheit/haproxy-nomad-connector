# Build stage
FROM golang:1.22-alpine AS builder

# Install ca-certificates and git
RUN apk add --no-cache ca-certificates git

# Set working directory
WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download && go mod verify

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -o haproxy-nomad-connector \
    ./cmd/haproxy-nomad-connector

# Final stage
FROM alpine:latest

# Install ca-certificates for HTTPS requests
RUN apk --no-cache add ca-certificates tzdata

# Create non-root user
RUN addgroup -g 1001 connector && \
    adduser -D -s /bin/sh -u 1001 -G connector connector

# Set working directory
WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/haproxy-nomad-connector .

# Change ownership to connector user
RUN chown connector:connector /app/haproxy-nomad-connector

# Switch to non-root user
USER connector

# Expose port (if needed for health checks)
EXPOSE 8080

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD ./haproxy-nomad-connector --version || exit 1

# Set entrypoint
ENTRYPOINT ["./haproxy-nomad-connector"]