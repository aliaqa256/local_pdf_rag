# Build stage
FROM golang:1.24-alpine AS builder

# Set working directory
WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main ./cmd/api

# Final stage
FROM alpine:latest

# Install ca-certificates and curl for health checks
RUN apk --no-cache add ca-certificates curl wget

# Create app directory
WORKDIR /root/

# Copy the binary and web files from builder stage
COPY --from=builder /app/main .
COPY --from=builder /app/web ./web

# Expose port 8090
EXPOSE 8090

# Run the application
CMD ["./main"]
