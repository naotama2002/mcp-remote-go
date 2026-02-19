# syntax=docker/dockerfile:1

# Build stage
FROM golang:1.26-alpine AS builder

# Install necessary packages
RUN apk add --no-cache git ca-certificates

# Set working directory
WORKDIR /app

# Copy go mod files
COPY go.mod ./
COPY go.sum* ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o mcp-remote-go ./cmd/mcp-remote-go

# Final stage
FROM alpine:latest

# Install ca-certificates for SSL/TLS
RUN apk --no-cache add ca-certificates

# Create non-root user
RUN addgroup -g 1001 -S appgroup && \
    adduser -S appuser -u 1001 -G appgroup

WORKDIR /app

# Copy binary from builder stage
COPY --from=builder /app/mcp-remote-go .

# Change ownership to non-root user
RUN chown -R appuser:appgroup /app

# Switch to non-root user
USER appuser

# Expose port (default port for MCP servers)
EXPOSE 3334

# Set entrypoint
ENTRYPOINT ["./mcp-remote-go"] 