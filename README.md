# PG Exporter

[Prometheus](https://prometheus.io/) [exporter](https://prometheus.io/docs/instrumenting/exporters/) for [PostgreSQL](https://www.postgresql.org) metrics

You can [download](https://github.com/Vonng/pg_exporter/releases) latest binaries on [release](https://github.com/Vonng/pg_exporter/releases) page


## Features
* Support both Postgres & Pgbouncer
* Flexible, completely customizable queries & metrics
* More control on execution policy (version, primay/standby, cluster/database level), cache, timeout



## Quick Start

```bash
export PG_EXPORTER_URL='postgres://postgres:password@localhost:5432/postgres'
export PG_EXPORTER_CONFIG='/path/to/conf/file/or/dir'
pg_exporter
```

Note that pg_export does not built in default metrics. All metrics are defined in customizable config files. By default, it will use [`./pg_exporter.yaml`](./pg_exporter.yaml) as config path. or you can specify a conf dir like [/etc/pg_exporter/conf](https://github.com/Vonng/pg_exporter/tree/master/conf) 

Or the docker way:

```bash
docker run \
  --env=PG_EXPORTER_URL='postgres://postgres:password@docker.for.mac.host.internal:5432/postgres' \
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
postgresql:///?sslmode=disable
```

If the target database name is `pgbouncer`, The exporter will turn into pgbouncer mode automatically. Provide you are not really going to create a real world "pgbouncer" database.



## Run

```bash
usage: pg_exporter [<flags>]

Flags:
  --help                         Show context-sensitive help (also try --help-long and --help-man).
  --url="postgresql:///?sslmode=disable"
                                 postgres connect url
  --config="./pg_exporter.yaml"  Path to config files
  --label=""                     Comma separated list label=value pair
  --disable-cache                force not using cache
  --web.listen-address=":8848"   prometheus web server listen address
  --web.telemetry-path="/metrics"
                                 Path under which to expose metrics.
  --log-level="Info"             log-level
  --explain                      dry run and explain queries
  --version                      Show application version.
  --log.level="info"             Only log messages with the given severity or above. Valid levels: [debug, info, warn, error, fatal]
  --log.format="logger:stderr"   Set the log target and format. Example: "logger:syslog?appname=bob&local=7" or "logger:stdout?json=true"
```

## Config

config files are using YAML format, there are lots of examples in the [conf](https://github.com/Vonng/pg_exporter/tree/master/conf) dir

here is a sample config that scrapes `pg_database` metrics:

```yaml
pg_database:
  query: |
    SELECT datname,
           pg_database_size(oid)      AS size,
           age(datfrozenxid)          AS age,
           datistemplate              AS is_template,
           datallowconn               AS allow_conn,
           datconnlimit               AS conn_limit,
           datfrozenxid::TEXT::BIGINT as frozen_xid
    FROM pg_database;
  ttl: 100
  tags: [cluster]
  min_version: 100000
  max_version: 130000
  metrics:
    - datname:
        usage: LABEL
        description: database name
    - size:
        usage: GAUGE
        description: database size in bytes
    - age:
        usage: GAUGE
        description: database age calculated by age(datfrozenxid)
    - is_template:
        usage: GAUGE
        description: 1 for template db , 0 for normal db
    - allow_conn:
        usage: GAUGE
        description: 1 allow connection, 0 does not allow
    - conn_limit:
        usage: GAUGE
        description: connection limit, -1 for no limit
    - frozen_xid:
        usage: GAUGE
        description: tuple with xmin below this will always be visable (until wrap around)

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

Or just [download](https://github.com/Vonng/pg_exporter/releases) latest prebuilt binaries on [release](https://github.com/Vonng/pg_exporter/releases) page



## About

Author：Vonng ([fengruohang@outlook.com](mailto:fengruohang@outlook.com))

License：BSD