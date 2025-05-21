# Build stage
FROM golang:1.24-bookworm AS builder

WORKDIR /build

# Copy all files to the build context
COPY . .

# Download dependencies
RUN go mod tidy

# Build the Go binary
RUN CGO_ENABLED=0 GOOS=linux go build -o ./app .

# Deploy the application binary
FROM gcr.io/distroless/base-debian12

# Copy the binary from the build stage
COPY --from=builder /build/app /app

# Run the application
ENTRYPOINT ["/app"]