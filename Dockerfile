# Build stage
FROM golang:1.21 AS builder
WORKDIR /app
COPY . .
RUN go mod init workload-discovery && \
    go get k8s.io/client-go@v0.29.0 && \
    go get github.com/prometheus/client_golang@v1.19.0 && \
    CGO_ENABLED=0 GOOS=linux go build -o workload-discovery .

# Runtime stage
FROM alpine:3.19
COPY --from=builder /app/workload-discovery /usr/local/bin/
RUN apk add --no-cache ca-certificates
USER 1001
CMD ["workload-discovery"]