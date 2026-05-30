# Build stage
FROM golang:1.23-alpine AS builder
WORKDIR /app

# Copy module files. Wildcard ensures it doesn't fail if go.sum is missing locally
COPY go.mod go.su[m] ./
RUN go mod download

COPY . .

# Compile with CGO disabled and stripping flags (-s -w) to create a drastically smaller binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o server ./cmd/server

# Runtime stage
FROM alpine:latest
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app

COPY --from=builder /app/server .
COPY migrations ./migrations

EXPOSE 7000 7001
CMD ["./server"]
