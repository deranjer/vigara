# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS go-deps
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

FROM --platform=$BUILDPLATFORM node:22-alpine AS web-build
WORKDIR /src
RUN corepack enable && corepack prepare pnpm@11.5.2 --activate
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml turbo.json ./
COPY apps/web/package.json ./apps/web/package.json
RUN pnpm install --frozen-lockfile --filter @vigira/web
COPY apps/web ./apps/web
RUN pnpm --filter @vigira/web build

FROM go-deps AS assemble
ARG TARGETOS
ARG TARGETARCH
COPY apps/api ./apps/api
COPY internal ./internal
COPY --from=web-build /src/apps/api/web ./apps/api/web
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/vigara ./apps/api/cmd/vigira

FROM alpine:3.22 AS runtime
RUN apk add --no-cache git openssh-client ca-certificates sops \
    && addgroup -S -g 10001 vigira && adduser -S -D -H -u 10001 -G vigira vigira \
    && mkdir -p /var/lib/vigira/android-backups /var/lib/vigira/stacks.git \
    && chown -R 10001:10001 /var/lib/vigira
COPY --from=assemble --chown=10001:10001 /out/vigara /usr/local/bin/vigara
COPY apps/web/src/compose-spec.LICENSE /usr/share/licenses/vigara/compose-spec.LICENSE
ENV HOME=/var/lib/vigira
USER 10001:10001
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/vigara"]
