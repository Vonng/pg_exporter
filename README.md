# PG Exporter

[Prometheus](https://prometheus.io/) [exporter](https://prometheus.io/docs/instrumenting/exporters/) for [PostgreSQL](https://www.postgresql.org) metrics. Latest binaries can be found on [release](https://github.com/Vonng/pg_exporter/releases) page. 

supporting PostgreSQL 10+ & Pgbouncer 1.9+.



## Features

* Support both Postgres & Pgbouncer
* Fine-grained execution control (cluster/database level, primary/standby only, tags, etc...)
* Flexible: completely customizable queries & metrics
* Configurable caching policy & query timeout
* Including many internal metrics, built-in self-monitoring
* Dynamic planning query, brings more fine-grained contronl on execution policy
* Auto discovery multi database in the same cluster 
* Tested in real world production environment (200+ Nodes)



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
  --env=PGB_EXPORTER_WEB_LISTEN_ADDRESS=':9630' \
  --env=PGB_EXPORTER_WEB_TELEMETRY_PATH='/metrics' \
  -p 8848:8848 \
  pg_exporter
```

The default listen address is `localhost:8848` and the default telemetry path is `/metrics`. 

```bash
curl localhost:9630/metrics
```

And the default data source name is:

```bash
postgresql:///?sslmode=disable
```

If the target database name is `pgbouncer`, The exporter will turn into pgbouncer mode automatically. Provide you are not really going to create a real world "pgbouncer" database.





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



## Run

```bash
usage: pg_exporter [<flags>]

Flags:
  --help                         Show context-sensitive help (also try --help-long and --help-man).
  --url=URL                      postgres connect url
  --config="./pg_exporter.yaml"  Path to config files
  --label=""                     Comma separated list label=value pair
  --disable-cache                force not using cache
  --namespace=""                 prefix of built-in metrics
  --web.listen-address=":8848"   prometheus web server listen address
  --web.telemetry-path="/metrics"  
                                 Path under which to expose metrics.
  --explain                      dry run and explain queries
  --version                      Show application version.
  --log.level="info"             Only log messages with the given severity or above. Valid levels: [debug, info, warn, error, fatal]
  --log.format="logger:stderr"   Set the log target and format. Example: "logger:syslog?appname=bob&local=7" or "logger:stdout?json=true"

```



## Config

Config files are using YAML format, there are lots of examples in the [conf](https://github.com/Vonng/pg_exporter/tree/master/conf) dir

here is a sample config that scrapes `pg_database` metrics:

```yaml
pg_primary_only:
  name: pg
  query: |
    SELECT extract(EPOCH FROM CURRENT_TIMESTAMP)                  AS timestamp,
           pg_current_wal_lsn() - '0/0'                           AS lsn,
           pg_current_wal_insert_lsn() - '0/0'                    AS insert_lsn,
           pg_current_wal_lsn() - '0/0'                           AS write_lsn,
           pg_current_wal_flush_lsn() - '0/0'                     AS flush_lsn,
           extract(EPOCH FROM now() - pg_postmaster_start_time()) AS uptime,
           extract(EPOCH FROM now() - pg_conf_load_time())        AS conf_reload_time,
           pg_is_in_backup()                                      AS is_in_backup,
           extract(EPOCH FROM now() - pg_backup_start_time())     AS backup_time;

  ttl: 10
  tags: [cluster, primary]
  min_version: 100000
  skip_errors: true
  metrics:
    - timestamp:
        usage: GAUGE
        description: database current timestamp
    - lsn:
        usage: COUNTER
        description: log sequence number, current write location (on primary)
    - insert_lsn:
        usage: COUNTER
        description: primary only, location of current wal inserting
    - write_lsn:
        usage: COUNTER
        description: primary only, location of current wal writing
    - flush_lsn:
        usage: COUNTER
        description: primary only, location of current wal syncing
    - uptime:
        usage: GAUGE
        description: seconds since postmaster start
    - conf_reload_time:
        usage: GAUGE
        description: seconds since last configuration reload
    - is_in_backup:
        usage: GAUGE
        description: 1 if backup is in progress
    - backup_time:
        usage: GAUGE
        description: seconds since current backup start. null if don't have one

```

It is quiet simple, the `name` is used as prefix of corresponding metric name, The `query` is sending to PostgreSQL/Pgbouncer to retrieving metrics. `ttl` controls cache expiration timeout. `min_version` and `max_version` specified semver range of this query. And `skip_errors` flag will treat query error as non-fatal error. And `tags`: with `cluster` tag, this query will not repeatly execute on different database in a same cluster. And with `primary` tag, this query will not execute on standby servers, `standby` tag, vice versa. And pgbouncer query have a special tag `pgbouncer`, those query will run if and only if target server is a pgbouncer (which is indicated by database name `pgbouncer`, so in case if you connect a real world PostgreSQL database named `pgbouncer`, this would be funny...)

Queries are planned after server's fact are gathered. Server's `version`, `in_recovery` and other metadata are scrapped before running any query. But there will still be a very little chance that server fact changed between meta fetching and query execution. 

If a directory is given, `pg_exporter` will iterate the directory and load config in alphabetic order.

You can provide multiple config file even in split conf mode.  AND you could provide multiple version of a metric query like `pg_db_10`, `pg_db_93`. The Server.Plan will discard query that does not matching server's fact. So in order to make sure no duplicated metrics are gathered, please make sure all query versions are mutually exclusive by (`primary|standby`, exclusive version range, etc....).

Note that most built-in queries are trying to make no assumption on target servers. while `pg_query` depending on built-in extension `pg_stat_statements` installed in schema `monitor`. and `pg_size.wal|log` requires wal & log dir exist. Normally these queries errors are skipped



## FAQ



## About

Author：Vonng ([fengruohang@outlook.com](mailto:fengruohang@outlook.com))

License：BSD

**Regards**: I used to be a faithful user of [PostgresExporter](https://github.com/wrouesnel/postgres_exporter), until I felt some functionailities are missing. Part of API is compatible with that, and the ideas are blatantly copy from it . And, of course, with some additional features. 😆

