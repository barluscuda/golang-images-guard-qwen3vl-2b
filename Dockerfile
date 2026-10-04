FROM golang:1.27.1-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate

FROM alpine:3.22
RUN apk add --no-cache ca-certificates \
    && addgroup -g 10001 guard \
    && adduser -D -H -u 10001 -G guard guard \
    && mkdir -p /app/config /data/images \
    && chown -R guard:guard /app /data
WORKDIR /app
COPY --from=builder /out/api /app/api
COPY --from=builder /out/worker /app/worker
COPY --from=builder /out/migrate /app/migrate
COPY --chown=guard:guard config /app/config
COPY --chown=guard:guard migrations /app/migrations
USER guard
EXPOSE 8080
ENTRYPOINT ["/app/api"]
