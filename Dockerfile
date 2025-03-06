# # Build stage
# FROM cgr.dev/chainguard/go:latest-dev AS builder
# WORKDIR /app
# COPY discovery.go .
# USER root
# RUN go mod init go-discover && \
#     go mod tidy && \
#     go get k8s.io/client-go@v0.19.0 && \
#     go get github.com/prometheus/client_golang@v1.19.0 && \
#     CGO_ENABLED=0 GOOS=linux go build -o go-discover .

# # Runtime stage
# FROM cgr.dev/ky-rafaels.example.com/chainguard-base:20230214
# WORKDIR /
# COPY --from=builder /app/go-discover .
# # RUN apk add --no-cache ca-certificates
# CMD ["go-discover"]

# Build stage
# FROM golang:1.24.1 AS builder
FROM cgr.dev/chainguard/go:latest-dev AS builder
WORKDIR /app
COPY discover.go .
RUN go mod init workload-discovery && \
    go mod tidy && \
    # go get k8s.io/client-go@v0.30.0 && \
    # go get github.com/prometheus/client_golang@v1.20.0 && \
    CGO_ENABLED=0 GOOS=linux go build -o workload-discovery .

# Runtime stage
FROM cgr.dev/ky-rafaels.example.com/chainguard-base:20230214
COPY --from=builder /app/workload-discovery /usr/local/bin/
# RUN apk add --no-cache ca-certificates
# USER nobody
CMD ["workload-discovery"]