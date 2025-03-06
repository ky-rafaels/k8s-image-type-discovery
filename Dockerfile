FROM cgr.dev/chainguard/go:latest-dev AS builder
WORKDIR /app
COPY discover.go .
RUN go mod init workload-discovery && \
    go mod tidy && \
    CGO_ENABLED=0 GOOS=linux go build -o workload-discovery .

# Runtime stage
FROM cgr.dev/ky-rafaels.example.com/chainguard-base:20230214
COPY --from=builder /app/workload-discovery /usr/local/bin/
CMD ["workload-discovery"]