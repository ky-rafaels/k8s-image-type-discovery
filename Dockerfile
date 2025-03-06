# Build stage
FROM cgr.dev/chainguard/go:latest-dev AS builder
WORKDIR /app
COPY discovery.go .
USER root
RUN go mod init go-discover && \
    go get k8s.io/client-go@v0.29.0 && \
    go get github.com/prometheus/client_golang@v1.19.0 && \
    CGO_ENABLED=0 GOOS=linux go build -o go-discover .

# Runtime stage
FROM cgr.dev/ky-rafaels.example.com/chainguard-base:20230214
WORKDIR /
COPY --from=builder /app/go-discover .
# RUN apk add --no-cache ca-certificates
CMD ["go-discover"]