FROM golang:1.27.1-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/guard ./cmd/api

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 guard \
    && useradd --uid 10001 --gid guard --no-create-home guard \
    && mkdir -p /app/config /data/images \
    && chown -R guard:guard /app /data
WORKDIR /app
COPY --from=builder /out/guard /app/guard
COPY --chown=guard:guard config /app/config
USER guard
EXPOSE 8080
ENTRYPOINT ["/app/guard"]
