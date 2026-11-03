# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM node:24-alpine AS frontend
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci --ignore-scripts --no-audit --no-fund
COPY web ./web
COPY licenses ./licenses
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.26.4-alpine AS backend
ARG TARGETOS=linux
ARG TARGETARCH
RUN apk add --no-cache python3 ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /src/web/build ./web/build
RUN python3 scripts/prepare_vm.py --platform ${TARGETOS}-${TARGETARCH}
RUN mkdir -p /out && CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags='-s -w' -o /out/monitor .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && addgroup -g 10001 monitor && adduser -D -u 10001 -G monitor monitor && mkdir -p /data/plugin && chown 10001:10001 /data/plugin
COPY --from=backend /out/monitor /usr/local/bin/monitor
USER 10001:10001
WORKDIR /data/plugin
ENV MONITOR_CONFIG_PATH=/data/plugin/config.yaml
EXPOSE 3013
ENTRYPOINT ["/usr/local/bin/monitor"]
