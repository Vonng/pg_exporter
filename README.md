# PG Exporter

[Prometheus](https://prometheus.io/) [exporter](https://prometheus.io/docs/instrumenting/exporters/) for [PostgreSQL](https://www.postgresql.org) metrics. **Gives you complete insight on your favourate elephant!**

Latest binaries can be found on [release](https://github.com/Vonng/pg_exporter/releases) page. [Support](pg_exporter.yaml) for PostgreSQL 10+ & Pgbouncer 1.9+ is provided ([`conf`](conf/)).  Lower version support could be add in the future, or you can provide your own conf to achieve it.

Latest version is v0.1.2 Here are how self monitoring dashboards looks like:

![](doc/pg-exporter.png)



## Features

* Support Postgres & Pgbouncer (pgbouncer mode is enabled when target dbname is `pgbouncer`)
* Fine-grained execution control (Tags Filter, Facts Filter, Version Filter, Timeout, Cache, etc...)
* Flexible: Almost all metrics are defined in customizable config files with SQL query. 
* Dynamic Planning: User could provide multiple branches of a metric queries. Queries matches server version & fact & tag will be actually installed.
* Configurable caching policy & query timeout
* Rich metrics about `pg_exporter` itself.
* Auto discovery multi database in the same cluster (TBD) 
* Tested in real world production environment (200+ Nodes)
* Metrics overhelming!  Gives you complete insight on your favourate elephant!



## Quick Start

To run this exporter, you will need two things

* **Where** to scrape:  A postgres or pgbouncer URL given via `PG_EXPORTER_URL`  or `--url`
* **What** to scrape: A path to config file or directory, by default `./pg_exporter.yaml`

```bash
export PG_EXPORTER_URL='postgres://postgres:password@localhost:5432/postgres'
export PG_EXPORTER_CONFIG='/path/to/conf/file/or/dir'
pg_exporter
```

`pg_exporter` only built-in with 3 metrics: `pg_up`,`pg_version` , and  `pg_in_recovery`. **All other metrics are defined in configuration files** . You cound use pre-defined configuration file: [`pg_exporter.yaml`](pg_exporter.yaml) or use separated metric query in [conf](https://github.com/Vonng/pg_exporter/tree/master/conf)  dir.

## Run

Parameter could be given via command line arguments or environment variables. 

```bash
usage: pg_exporter [<flags>]

Flags:
  --help                        Show context-sensitive help (also try --help-long and --help-man).
  --url=URL                     postgres target url
  --config="pg_exporter.yaml"   path to config dir or file
  --label=""                    constant lables: separated list label=value pair
  --tag=""                      tags, separated list of server tag
  --disable-cache               force not using cache
  --auto-discovery              automatically scrape all database for given server (TBD)
  --exclude-database="postgres,template0,template1"
                                excluded databases when enabling auto-discovery
  --namespace=""                prefix of built-in metrics, (pg|pgbouncer) by default
  --fail-fast                   fail fast instead of waiting during start-up
  --web.listen-address=":9630"  prometheus web server listen address
  --web.telemetry-path="/metrics"
                                URL path under which to expose metrics.
  --dry-run                     dry run and print raw configs
  --explain                     explain server planned queries
  --version                     Show application version.
  --log.level="info"            Only log messages with the given severity or above. Valid levels: [debug, info, warn, error, fatal]
  --log.format="logger:stderr"  Set the log target and format. Example: "logger:syslog?appname=bob&local=7" or "logger:stdout?json=true"
```

* `--url` or `PG_EXPORTER_URL` defines **where** to scrape, it should be a valid DSN or URL. (note that `sslmode=disable` must be specifed explicitly for database that does not using SSL)
* `--config` or `PG_EXPORTER_CONFIG` defines **how** to scrape. It could be a single yaml files or a directory contains a series of separated yaml config. In the later case, config will be load in alphabetic order.
* `--label` or `PG_EXPORTER_LABEL` defines **constant labels** that are added into all metrics. It should be a comma separated list of `label=value` pair.
* `--tag` or `PG_EXPORTER_TAG` will mark this exporter with given tags. Tags are comma separated list of string. which could be used for query filtering and execution control.
* `--disable-cache` or `PG_EXPORTER_DISABLE_CACHE` will disable metric cache.
* `--auto-discovery` or `PG_EXPORTER_AUTO_DISCOVERY` will automatically spawn peripheral servers for other databases in target PostgreSQL server. except for those listed in `--exclude-databse`. (Not implemented yet)
* `--exclude-database`  or `PG_EXPORTER_EXCLUDE_DATABASE` is a comma separated list of database name. Which are not scrapped when `--auto-discovery` is enabled
* `--namespace` or `PG_EXPORTER_NAMESPACE` defineds **internal metrics prefix**, by default  `pg|pgbouncer`.
* `--fail-fast` or `PG_EXPORTER_FAIL_FAST` is a flag. During start-up, `pg_exporter` will wait if target is down. with `--fail-fast=true`, `pg_exporter` will fail instead of wait on start-up procedure if target is down
* `--listenAddress` or `PG_EXPORTER_LISTEN_ADDRESS` is the endpoint that expose metrics
* `--metricPath` or `PG_EXPORTER_TELEMETRY_PATH` is the URL path under which to expose metrics.
* `--dry-run` will print configuration files
* `--explain` will actually connect to target server and planning queries for it. Then explain which queries are installed.

#### Debug Interface

```bash
# Reload configuration
curl localhost:9630/reload

# Explain configuration
curl localhost:9630/explain
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

Or [download](https://github.com/Vonng/pg_exporter/releases) latest prebuilt binaries on [release](https://github.com/Vonng/pg_exporter/releases) page



## Config

Configs are core part of `pg_exporter`. Actually this project contains more lines of YAML than go.

* A monolith battery-include configuration file: [`pg_exporter.yaml`](pg_exporter.yaml)
* Separated metrics definition in [`conf`](conf/)
* Example of how to write a config files:  [`doc.txt`](conf/100-doc.txt)

### built-in configs

Dashboard for monitoring pg_exporter itself: [`pg-exporter.json`](monitor/pg-exporter.json)

![](doc/pg-exporter.png)

Current `pg_exporter` is ship with 32 built-in metrics queries. 


* [pg](conf/101-pg.yaml)
* [pg_meta](conf/102-pg_meta.yaml)
* [pg_bgwriter](conf/103-pg_bgwriter.yaml)
* [pg_checkpoint](conf/104-pg_checkpoint.yaml)
* [pg_recovery](conf/105-pg_recovery.yaml)
* [pg_standby](conf/106-pg_standby.yaml)
* [pg_repl](conf/107-pg_repl.yaml)
* [pg_repl_state](conf/108-pg_repl_state.yaml)
* [pg_slot](conf/109-pg_slot.yaml)
* [pg_activity](conf/110-pg_activity.yaml)
* [pg_wait](conf/111-pg_wait.yaml)
* [pg_xact](conf/112-pg_xact.yaml)
* [pg_lock](conf/113-pg_lock.yaml)
* [pg_vacuuming](conf/114-pg_vacuuming.yaml)
* [pg_indexing](conf/115-pg_indexing.yaml)
* [pg_clustering](conf/116-pg_clustering.yaml)
* [pg_query](conf/117-pg_query.yaml)
* [pg_size](conf/118-pg_size.yaml)
* [pg_database](conf/119-pg_database.yaml)
* [pg_db](conf/120-pg_db.yaml)
* [pg_class](conf/121-pg_class.yaml)
* [pg_table](conf/122-pg_table.yaml)
* [pg_table_size](conf/123-pg_table_size.yaml)
* [pg_defpart](conf/124-pg_defpart.yaml)
* [pg_table_bloat](conf/125-pg_table_bloat.yaml)
* [pg_index](conf/126-pg_index.yaml)
* [pg_index_bloat](conf/127-pg_index_bloat.yaml)
* [pg_func](conf/128-pg_func.yaml)
* [pgbouncer_list](conf/129-pgbouncer_list.yaml)
* [pgbouncer_stat](conf/130-pgbouncer_stat.yaml)
* [pgbouncer_database](conf/131-pgbouncer_database.yaml)
* [pgbouncer_pool](conf/132-pgbouncer_pool.yaml)

`pg_exporter` will generate approximately 200~300 metrics for completely new database cluster. For a real-world database with 10 ~ 100 tables, it may generate serveral 1k ~ 10k metrics. You may need modifying or disable some  database-level metrics on database with serveral thousands or more tables in order to complete scrape in time.

Config files are using YAML format, there are lots of examples in the [conf](https://github.com/Vonng/pg_exporter/tree/master/conf) dir. and here is a [sample](conf/100-doc.txt) config.

```yaml
#  pg_primary_only:       <---- Branch name, distinguish different branch of a metric query
#    name: pg             <---- actual Query name, used as metric prefix, will set to branch if not provide
#    desc: PostgreSQL basic information (on primary)                    <---- query description
#    query: |                                                           <---- query string
#
#      SELECT extract(EPOCH FROM CURRENT_TIMESTAMP)                  AS timestamp,
#             pg_current_wal_lsn() - '0/0'                           AS lsn,
#             pg_current_wal_insert_lsn() - '0/0'                    AS insert_lsn,
#             pg_current_wal_lsn() - '0/0'                           AS write_lsn,
#             pg_current_wal_flush_lsn() - '0/0'                     AS flush_lsn,
#             extract(EPOCH FROM now() - pg_postmaster_start_time()) AS uptime,
#             extract(EPOCH FROM now() - pg_conf_load_time())        AS conf_reload_time,
#             pg_is_in_backup()                                      AS is_in_backup,
#             extract(EPOCH FROM now() - pg_backup_start_time())     AS backup_time;
#
#                                <---- following field are [OPTIONAL], control execution policy
#    ttl: 10                     <---- cache ttl: how long will exporter cache it's result. set to 0 to disable cache
#    timeout: 0.1                <---- timeout: in seconds, query execeed this will be canceled. default is 0.1, set to -1 to disable timeout
#    min_version: 100000         <---- minimal supported version in server version number format, e.g  120001 = 12.1, 090601 = 9.6.1
#    max_version: 130000         <---- maximal supported version in server version number format, boundary not include
#    fatal: false                <---- if query marked fatal fail, this scrape will abort immidiately
#
#    tags: [cluster, primary]    <---- tags consist of one or more string, which could be:
#                                        * 'cluster' marks this query as cluster level, so it will only execute once for same PostgreSQL Server
#                                        * 'primary' marks this query can only run on a master instance (will not execute if pg_is_in_recovery())
#                                        * 'standby' marks this query can only run on a recoverying instance (will execute if pg_is_in_recovery())
#                                        * some special tag prefix will have special effect:
#                                        * 'dbname:<dbname>' means this query will only execute on database with name '<dbname>'
#                                        * 'username:<user>' means this query will only execute when connect with user '<user>'
#                                        * 'extension:<extname>' means this query will only execute when extension '<extname>' is installed
#                                        * 'schema:<nspname>' means this query will only execute when schema '<nspname>' exist
#                                        * 'not:<negtag>' means this query will only execute when exporter is launch without tag '<negtag>'
#                                        * '<tag>' means this query will only execute when exporter is launch with tag '<tag>'
#                                           (tag could not be cluster,primary,standby or have special prefix)
#
#
#    metrics:                    <---- this is a list of returned columns, each column must have a name, usage, could have an alias and description
#      - timestamp:              <---- this is column name, should be exactly same as returned column name
#          rename: ts            <---- rename is optional, will use this alias instead of column name
#          usage: GAUGE          <---- usage could be
#                                        * DISCARD: completely ignore this field
#                                        * LABEL: use columnName:columnValue as a label in result
#                                        * GAUGE: use this column as a metric, which is '<query.name>_<column.name>{<labels>} column.value'
#                                        * COUNTER: same as GAUGE, except it is a counter.
#
#          description: database current timestamp
#      - lsn:
#          usage: COUNTER
#          description: log sequence number, current write location (on primary)
#      - insert_lsn:
#          usage: COUNTER
#          description: primary only, location of current wal inserting
#      - write_lsn:
#          usage: COUNTER
#          description: primary only, location of current wal writing
#      - flush_lsn:
#          usage: COUNTER
#          description: primary only, location of current wal syncing
#      - uptime:
#          usage: GAUGE
#          description: seconds since postmaster start
#      - conf_reload_time:
#          usage: GAUGE
#          description: seconds since last configuration reload
#      - is_in_backup:
#          usage: GAUGE
#          description: 1 if backup is in progress
#      - backup_time:
#          usage: GAUGE
#          description: seconds since current backup start. null if don't have one
```



## Visualize

Users could visualize these metrics via fancy grafana dashboards.

![](doc/pg-dashboards.png)

![](doc/pg-instance.png)

![](doc/pg-cluster.png)

![](pg-exporter-overview.png)



## About

Author：Vonng ([fengruohang@outlook.com](mailto:fengruohang@outlook.com))

License：BSD

