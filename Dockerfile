# syntax=docker/dockerfile:1
FROM golang:1.22-alpine AS builder-env

# Build a self-contained pg_exporter container with a clean environment and no
# dependencies.
#
# build with
#
#   docker buildx build -f Dockerfile --tag pg_exporter .
#

WORKDIR /build

COPY go.mod go.sum ./
RUN \
  --mount=type=cache,target=/go/pkg/mod \
  --mount=type=cache,target=/root/.cache/go-build \
  CGO_ENABLED=0 GOOS=linux go mod download

COPY . /build
RUN \
  --mount=type=cache,target=/go/pkg/mod \
  --mount=type=cache,target=/root/.cache/go-build \
  CGO_ENABLED=0 GOOS=linux go build -a -o /pg_exporter .

FROM scratch
LABEL org.opencontainers.image.authors="Vonng <rh@vonng.com>, Craig Ringer <craig.ringer@enterprisedb.com>" \
      org.opencontainers.image.url="https://github.com/Vonng/pg_exporter" \
      org.opencontainers.image.source="https://github.com/Vonng/pg_exporter" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.title="pg_exporter" \
      org.opencontainers.image.description="PostgreSQL metrics exporter for Prometheus"

WORKDIR /bin
COPY --from=builder-env /pg_exporter /bin/pg_exporter
COPY pg_exporter.yml /etc/pg_exporter.yml
EXPOSE 9630/tcp
ENTRYPOINT ["/bin/pg_exporter"]
