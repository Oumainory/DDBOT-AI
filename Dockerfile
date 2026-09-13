# DDBOT-AI Full image.  FFmpeg is an external executable in the runtime
# image; the Go binary itself is always built with CGO disabled.
FROM golang:1.26.2-bookworm AS builder

ARG RELEASE_VERSION=dev
ARG RELEASE_COMMIT=unknown
ARG RELEASE_TIME=unknown
ARG TARGETARCH=amd64

WORKDIR /src
COPY go.mod go.sum ./
# The root module uses local replacements. Copy their module manifests before
# downloading dependencies so the cacheable download step can resolve those
# replacements without requiring the full source tree.
COPY adapter/go.mod adapter/go.sum ./adapter/
COPY bot/go.mod bot/go.sum ./bot/
COPY lsp/eventbus/go.mod ./lsp/eventbus/
COPY utils/qqlog/go.mod utils/qqlog/go.sum ./utils/qqlog/
RUN go mod download
COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w \
      -X github.com/Oumainory/DDBOT-AI/internal/buildinfo.Version=${RELEASE_VERSION} \
      -X github.com/Oumainory/DDBOT-AI/internal/buildinfo.Commit=${RELEASE_COMMIT} \
      -X github.com/Oumainory/DDBOT-AI/internal/buildinfo.BuildTime=${RELEASE_TIME} \
      -X github.com/Oumainory/DDBOT-AI/lsp.CommitId=${RELEASE_COMMIT} \
      -X github.com/Oumainory/DDBOT-AI/lsp.BuildTime=${RELEASE_TIME}" \
      -o /out/ddbot-ai ./cmd

FROM debian:bookworm-slim

ARG FFMPEG_VERSION
ARG FFMPEG_PROVIDER
ARG FFMPEG_PROVIDER_RELEASE
ARG FFMPEG_SOURCE_COMMIT
ARG FFMPEG_VARIANT
ARG FFMPEG_URL
ARG FFMPEG_SHA256
ARG FFMPEG_CHECKSUMS_URL
ARG FFMPEG_LICENSE_URL
ARG FFMPEG_LICENSE_TEXT_URL

RUN set -eux; \
    test -n "$FFMPEG_PROVIDER"; \
    test -n "$FFMPEG_PROVIDER_RELEASE"; \
    test -n "$FFMPEG_VERSION"; \
    test -n "$FFMPEG_SOURCE_COMMIT"; \
    test "$FFMPEG_VARIANT" = "lgpl-static"; \
    test -n "$FFMPEG_URL"; \
    test -n "$FFMPEG_SHA256"; \
    test -n "$FFMPEG_CHECKSUMS_URL"; \
    test -n "$FFMPEG_LICENSE_URL"; \
    test -n "$FFMPEG_LICENSE_TEXT_URL"; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates curl tar unzip xz-utils; \
    rm -rf /var/lib/apt/lists/*; \
    mkdir -p /tmp/ffmpeg /usr/local/bin /app; \
    curl --fail --location --silent --show-error "$FFMPEG_URL" -o /tmp/ffmpeg/archive; \
    echo "$FFMPEG_SHA256  /tmp/ffmpeg/archive" | sha256sum -c -; \
    case "$FFMPEG_URL" in \
      *.zip) unzip -q /tmp/ffmpeg/archive -d /tmp/ffmpeg/unpacked ;; \
      *.tar.gz|*.tgz) mkdir -p /tmp/ffmpeg/unpacked; tar -xzf /tmp/ffmpeg/archive -C /tmp/ffmpeg/unpacked ;; \
      *.tar.xz) mkdir -p /tmp/ffmpeg/unpacked; tar -xJf /tmp/ffmpeg/archive -C /tmp/ffmpeg/unpacked ;; \
      *) mkdir -p /tmp/ffmpeg/unpacked; cp /tmp/ffmpeg/archive /tmp/ffmpeg/unpacked/ffmpeg ;; \
    esac; \
    ffmpeg_path="$(find /tmp/ffmpeg/unpacked -type f \( -name ffmpeg -o -name ffmpeg.exe \) -print -quit)"; \
    test -n "$ffmpeg_path"; \
    install -m 0755 "$ffmpeg_path" /usr/local/bin/ffmpeg; \
    curl --fail --location --silent --show-error "$FFMPEG_LICENSE_TEXT_URL" -o /app/FFMPEG-LGPL-2.1.txt; \
    test -s /app/FFMPEG-LGPL-2.1.txt; \
    printf '%s\n' \
      "provider=$FFMPEG_PROVIDER" \
      "provider_release=$FFMPEG_PROVIDER_RELEASE" \
      "ffmpeg_version=$FFMPEG_VERSION" \
      "ffmpeg_source_commit=$FFMPEG_SOURCE_COMMIT" \
      "variant=$FFMPEG_VARIANT" \
      "asset_url=$FFMPEG_URL" \
      "asset_sha256=$FFMPEG_SHA256" \
      "upstream_checksums=$FFMPEG_CHECKSUMS_URL" \
      "license_url=$FFMPEG_LICENSE_URL" \
      "upstream=https://ffmpeg.org/" \
      "build_provider=https://github.com/BtbN/FFmpeg-Builds" \
      "runtime=external-executable" > /app/FFMPEG-PROVENANCE.txt; \
    rm -rf /tmp/ffmpeg

WORKDIR /data
COPY --from=builder /out/ddbot-ai /usr/local/bin/ddbot-ai
COPY LICENSE NOTICE THIRD_PARTY_NOTICES.md /app/

ENV DDBOT_AI_RUNTIME_MODE=docker \
    DDBOT_AI_HTTP_LISTEN=0.0.0.0:15631 \
    DDBOT_AI_PLATFORM_DB=/data/ddbot-ai.sqlite

VOLUME ["/data"]
EXPOSE 15631
ENTRYPOINT ["/usr/local/bin/ddbot-ai"]
