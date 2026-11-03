# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH

RUN CGO_ENABLED=0 \
    GOOS=${TARGETOS:-linux} \
    GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath \
      -ldflags "-s -w -X github.com/CNife/flyer/internal/cli.Version=${VERSION}" \
      -o /out/flyer ./cmd/flyer

FROM alpine:3.24

# hadolint ignore=DL3018
RUN apk add --no-cache ca-certificates \
    && addgroup -g 10001 flyer \
    && adduser -D -u 10001 -G flyer -h /data -s /sbin/nologin flyer \
    && mkdir -p /data \
    && chown 10001:10001 /data

COPY --from=build /out/flyer /usr/local/bin/flyer
COPY deploy/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod 0755 /usr/local/bin/flyer /usr/local/bin/docker-entrypoint.sh

USER 10001:10001

VOLUME ["/data"]
EXPOSE 8787

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD ["wget", "-qO-", "http://127.0.0.1:8787/health"]

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
