FROM golang:1.25-bookworm AS builder
WORKDIR /workspace

COPY src/go.mod ./src/go.mod
WORKDIR /workspace/src
RUN go mod download

COPY src ./
# RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/container-monitor .

# FROM debian:bookworm-slim
# WORKDIR /app

# RUN apt-get update \
# 	&& apt-get install -y --no-install-recommends ca-certificates tzdata \
# 	&& rm -rf /var/lib/apt/lists/*

# COPY --from=builder /out/container-monitor /usr/local/bin/container-monitor

# ENTRYPOINT ["/usr/local/bin/container-monitor"]