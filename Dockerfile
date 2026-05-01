# syntax=docker/dockerfile:1.7

# ---- 1. Build the React management UI into a single-file management.html ----
FROM node:20-alpine AS web-builder

WORKDIR /web

# Install dependencies first to maximise Docker layer cache reuse.
COPY client/package.json client/package-lock.json ./
RUN npm ci --no-audit --no-fund --prefer-offline

# Build the bundled UI. vite-plugin-singlefile inlines all assets into dist/index.html.
COPY client/ ./
ARG VERSION=dev
ENV VERSION=${VERSION}
RUN npm run build

# ---- 2. Compile the Go proxy server ----
FROM golang:1.26-alpine AS go-builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" \
    -o ./CLIProxyAPI ./cmd/server/

# ---- 3. Runtime image ----
FROM alpine:3.22.0

RUN apk add --no-cache tzdata ca-certificates curl \
    && mkdir -p /CLIProxyAPI/static /CLIProxyAPI/auths /CLIProxyAPI/logs

WORKDIR /CLIProxyAPI

COPY --from=go-builder /app/CLIProxyAPI /CLIProxyAPI/CLIProxyAPI
COPY --from=web-builder /web/dist/index.html /CLIProxyAPI/static/management.html
COPY config.example.yaml /CLIProxyAPI/config.example.yaml
COPY config.production.yaml /CLIProxyAPI/config.production.yaml
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

# Tell the Go server where the locally built UI lives so it never reaches out to GitHub.
ENV MANAGEMENT_STATIC_PATH=/CLIProxyAPI/static \
    TZ=Etc/UTC

RUN cp /usr/share/zoneinfo/${TZ} /etc/localtime && echo "${TZ}" > /etc/timezone

EXPOSE 8317 1455 8085 54545 51121

HEALTHCHECK --interval=15s --timeout=3s --start-period=10s --retries=3 \
    CMD curl -fsS http://127.0.0.1:8317/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["./CLIProxyAPI"]
