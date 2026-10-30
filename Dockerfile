# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:bookworm AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/images-mcp .

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata curl \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd -g 10001 nonroot \
    && useradd -u 10001 -g nonroot -M -s /usr/sbin/nologin nonroot
COPY --from=builder /out/images-mcp /usr/local/bin/images-mcp
USER 10001:10001
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 CMD curl -fsS http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["images-mcp"]
CMD ["-http", ":8080"]
