# syntax=docker/dockerfile:1.7
FROM node:24-alpine AS web
WORKDIR /src
COPY web/package.json web/package-lock.json ./web/
RUN cd web && npm ci
COPY web ./web
COPY internal/webui ./internal/webui
RUN cd web && npm run build

FROM golang:1.25-alpine3.22 AS backend
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/webui/dist ./internal/webui/dist
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/cloud ./cmd/server

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 cloud \
    && adduser -S -D -H -u 10001 -G cloud cloud \
    && mkdir -p /data \
    && chown cloud:cloud /data
COPY --from=backend /out/cloud /usr/local/bin/cloud
USER cloud
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/cloud"]
