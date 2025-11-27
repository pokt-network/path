# TODO_TECHDEBT: Consolidate Dockerfile.local and Dockerfile
# Builder stage
FROM golang:1.24-alpine3.20 AS builder

# Install necessary build dependencies
RUN apk add --no-cache git make build-base

# Set working directory
WORKDIR /go/src/github.com/pokt-network/path

# Copy only go.mod and go.sum first to leverage Docker's build cache
COPY go.mod go.sum ./
RUN go mod download && go mod verify

# Set build flags for faster compilation
ENV CGO_ENABLED=0
ENV GOOS=linux
ENV GOARCH=amd64
ENV GO111MODULE=on

# Copy the entire codebase in one layer
# This is simpler and won't fail if certain directories don't exist
COPY . .

# Build with optimization flags
RUN go build -ldflags="-s -w" -o /go/bin/path ./cmd

# Final stage
FROM alpine:3.19 AS final

# Create non-root user
RUN addgroup -S appgroup && adduser -S appuser -G appgroup

# Set working directory
WORKDIR /app

# Add runtime dependencies and prepare directories
RUN apk add --no-cache ca-certificates tzdata && \
    mkdir -p /app/config && \
    chown -R appuser:appgroup /app

ARG IMAGE_TAG
ENV IMAGE_TAG=${IMAGE_TAG}

# Copy binary from builder stage
COPY --from=builder /go/bin/path ./

# Set the binary as executable
RUN chmod +x /app/path

# Use non-root user
USER appuser

# Add health check (only if your application supports it, remove if not)
# HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 CMD [ "/app/path", "--health-check" ] || exit 1

# Command to run
CMD ["./path"]
