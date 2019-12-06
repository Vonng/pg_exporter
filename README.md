# PG Exporter

Prometheus exporter for [PostgreSQL](https://www.postgresql.org) metrics

Tested on postgresql 10 ~ 12. lower version is supported but you have to write your own queries

This program is still under developing


## Features
* completely customizable queries, metrics are completely determined by config
* customizable execution policy for individual queries (cache, timeout, primary/standby)  
* avoid running cluster-level queries on multiple database instance

## Quick Start

You can do it the bare metal way:

```bash
PG_EXPORTER_DSN='postgres://postgres:password@localhost:5432/postgres' pg_exporter
```

Or the docker way:

```bash
docker run \
  --env=PG_EXPORTER_DSN='postgres://postgres:password@docker.for.mac.host.internal:5432/postgres' \
  --env=PGB_EXPORTER_WEB_LISTEN_ADDRESS=':8848' \
  --env=PGB_EXPORTER_WEB_TELEMETRY_PATH='/metrics' \
  -p 8848:8848 \
  pg_exporter
```

The default listen address is `localhost:8848` and the default telemetry path is `/metrics`. 

```bash
curl localhost:8848/metrics
```

And the default data source name is:

```bash
postgres://:5432/postgres?host=/tmp&sslmode=disable
```



## Build

```
go build
```

To build a static stand alone binary for docker scratch

```bash
CGO_ENABLED=0 GOOS=linux go build -a -ldflags '-extldflags "-static"' -o pg_exporter
```

To build a docker image, using:

```
docker build -t pg_exporter .
```


## Road Map

* integrate with pgbouncer support
* add optional pg settings support
* 

## About

Author：Vonng ([fengruohang@outlook.com](mailto:fengruohang@outlook.com))

License：BSD