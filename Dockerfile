# DDBOT-AI Full image.  FFmpeg is an external executable in the runtime
# image; the Go binary itself is always built with CGO disabled.
FROM golang:1.26.2-bookworm AS builder

ARG RELEASE_VERSION=dev
ARG RELEASE_COMMIT=unknown
ARG RELEASE_TIME=unknown
ARG TARGETARCH=amd64

WORKDIR /src
COPY go.mod go.sum ./
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
ARG FFMPEG_URL
ARG FFMPEG_SHA256
ARG FFMPEG_LICENSE_URL

RUN set -eux; \
    test -n "$FFMPEG_VERSION"; \
    test -n "$FFMPEG_URL"; \
    test -n "$FFMPEG_SHA256"; \
    test -n "$FFMPEG_LICENSE_URL"; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates curl tar unzip xz-utils; \
    rm -rf /var/lib/apt/lists/*; \
    mkdir -p /tmp/ffmpeg /usr/local/bin; \
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
