# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/root/.cache/go-build go test ./...
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/gitlabvars ./cmd/server

FROM alpine:3.23
ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED
ARG SOURCE_URL
LABEL org.opencontainers.image.title="GitLab CI Variables" \
      org.opencontainers.image.description="Project and group CI/CD variable management for GitLab" \
      org.opencontainers.image.authors="Kalinin I. A." \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${CREATED}" \
      org.opencontainers.image.source="${SOURCE_URL}"
RUN apk add --no-cache ca-certificates \
    && addgroup -S -g 10001 app \
    && adduser -S -D -H -u 10001 -G app app \
    && mkdir /data \
    && chown 10001:10001 /data \
    && chmod 700 /data
COPY --from=build /out/gitlabvars /usr/local/bin/gitlabvars
USER 10001:10001
ENV APP_ADDR=0.0.0.0:8080 \
    TRACKING_FILE=/data/tracking.json
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1
STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/local/bin/gitlabvars"]
