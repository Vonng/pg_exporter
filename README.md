# PG Exporter


[![Webite: pigsty](https://img.shields.io/badge/website-pgsty.com-slategray?style=flat&logo=cilium&logoColor=white)](https://pgsty.com)
[![Version: v0.8.0](https://img.shields.io/badge/version-v0.8.0-slategray?style=flat&logo=cilium&logoColor=white)](https://github.com/Vonng/pg_exporter/releases/tag/v0.8.0)
[![License: Apache-2.0](https://img.shields.io/github/license/Vonng/pg_exporter?logo=opensourceinitiative&logoColor=green&color=slategray)](https://github.com/Vonng/pg_exporter/blob/main/LICENSE)
[![GitHub Stars](https://img.shields.io/github/stars/Vonng/pg_exporter?style=flat&logo=github&logoColor=black&color=slategray)](https://star-history.com/#Vonng/pg_exporter&Date)

[Prometheus](https://prometheus.io/) [Exporter](https://prometheus.io/docs/instrumenting/exporters/) for [PostgreSQL](https://www.postgresql.org) & [pgBouncer](https://www.pgbouncer.org/) metrics.

PG Exporter aims to bring the ultimate observability for [Pigsty](https://pigsty.io), which is a **Battery-Included, Local-First PostgreSQL Distribution as an Open-Source RDS Alternative**: [Demo](https://demo.pigsty.cc) & [Gallery](https://github.com/Vonng/pigsty/wiki/Gallery)

PG Exporter is fully **customizable**: it defines almost all metrics with declarative YAML [configuration](pg_exporter.yml) files. It's easy to add new metrics or modify existing ones. Much more that the prometheus community one.

The latest stable version is [`0.8.0`](https://github.com/Vonng/pg_exporter/releases/tag/v0.8.0), which support PostgreSQL 10 ~ 17+ and Pgbouncer 1.8 ~ 1.24+. 

[![pigsty-v2-3](https://github.com/Vonng/pigsty/assets/8587410/ec2b8acb-d564-49ab-b7f0-214da176a7c8)](https://demo.pigsty.cc)



--------------------

## Features

* Support [Pigsty](https://pigsty.io), the PostgreSQL distribution with **ultimate observability**.
* Support both Postgres & Pgbouncer (Pgbouncer is detected when target dbname is `pgbouncer`)
* Flexible: Almost all metrics are defined in customizable conf files with SQL collector.
* Schedule: Fine-grained execution control: Timeout, Cache, Skip, Fatality, etc...
* Dynamic Planning: Define multiple branches for a collector. Install specific branch when server & exporter meet certain conditions.
* Rich [self-monitoring](https://demo.pigsty.cc/d/pgsql-exporter) metrics about `pg_exporter` itself.
* Auto-discovery multiple databases, and run database level collectors
* Tested and verified in a real-world production environment: 12K+ cores for 5+ years.



--------------------

## Quick Start

To run this exporter, you will need two things:

* **Where** to scrape:  A Postgres or pgbouncer URL given via `PG_EXPORTER_URL`  or `--url`.
* **What** to scrape: A path to config file or directory, by default `./pg_exporter.yml` or `/etc/pg_exporter.yml`

```bash
export PG_EXPORTER_URL='postgres://postgres:password@localhost:5432/postgres'
export PG_EXPORTER_CONFIG='/path/to/conf/file/or/dir'
pg_exporter
```

`pg_exporter` only built-in with 3 metrics: `pg_up`,`pg_version` , and  `pg_in_recovery`. **All other metrics are defined in configuration files**. 
You could use the pre-defined configuration file: [`pg_exporter.yml`](pg_exporter.yml) or use separated metric query in [conf](https://github.com/Vonng/pg_exporter/tree/master/config/collector)  dir.



--------------------

## Usage

Parameters could be given via command-line args or environment variables. 

* `--web.listen-address` is the web endpoint listen address, `:9630` by default, this parameter can not be changed via environment variable.
* `--web.telemetry-path or `PG_EXPORTER_TELEMETRY_PATH` is the URL path under which to expose metrics.
* `--url` or `PG_EXPORTER_URL` defines **where** to scrape, it should be a valid DSN or URL. (note that `sslmode=disable` must be specifed explicitly for database that does not using SSL)
* `--config` or `PG_EXPORTER_CONFIG` defines **how** to scrape. It could be a single YAML file or a directory containing a series of separated YAML configs, which config will be loaded in alphabetic order.
* `--label` or `PG_EXPORTER_LABEL` defines **constant labels** that are added to all metrics. It should be a comma-separated list of `label=value` pairs.
* `--tag` or `PG_EXPORTER_TAG` will mark this exporter with given tags. Tags are a comma-separated-value list of strings. which could be used for query filtering and execution control.
* `--disable-cache` or `PG_EXPORTER_DISABLE_CACHE` will disable metric cache.
* `--auto-discovery` or `PG_EXPORTER_AUTO_DISCOVERY` will automatically spawn peripheral servers for other databases in the target PostgreSQL server. except for those listed in `--exclude-database`. (Not implemented yet)
* `--exclude-database` or `PG_EXPORTER_EXCLUDE_DATABASE` is a comma-separated list of the database name. Which are not scrapped when `--auto-discovery` is enabled
* `--namespace` or `PG_EXPORTER_NAMESPACE` defined **internal metrics prefix**, by default `pg|pgbouncer`.
* `--fail-fast` or `PG_EXPORTER_FAIL_FAST` is a flag. During start-up, `pg_exporter` will wait if the target is down. with `--fail-fast=true`, `pg_exporter` will fail instead of waiting on the start-up procedure if the target is down
* `--connect-timeout` or `PG_EXPORTER_CONNECT_TIMEOUT` is the timeout for connecting to the target.
* `--dry-run` will print configuration files
* `--explain` will actually connect to the target server and plan queries for it. Then explain which queries are installed.
* `--log.level` will set logging level: one of `debug`, `info`, `warn`, `error`.



```bash
usage: pg_exporter [<flags>]

Flags:
  -h, --[no-]help            Show context-sensitive help (also try --help-long and --help-man).
  -u, --url=URL              postgres target url
  -c, --config=CONFIG        path to config dir or file
      --web.listen-address=:9630 ...
                             Addresses on which to expose metrics and web interface. Repeatable for multiple addresses.
      --web.config.file=""   [EXPERIMENTAL] Path to configuration file that can enable TLS or authentication. See: https://github.com/prometheus/exporter-toolkit/blob/master/docs/web-configuration.md
  -l, --label=""             constant lables:comma separated list of label=value pair ($PG_EXPORTER_LABEL)
  -t, --tag=""               tags,comma separated list of server tag ($PG_EXPORTER_TAG)
  -C, --[no-]disable-cache   force not using cache ($PG_EXPORTER_DISABLE_CACHE)
  -m, --[no-]disable-intro   disable collector level introspection metrics ($PG_EXPORTER_DISABLE_INTRO)
  -a, --[no-]auto-discovery  automatically scrape all database for given server ($PG_EXPORTER_AUTO_DISCOVERY)
  -x, --exclude-database="template0,template1,postgres"
                             excluded databases when enabling auto-discovery ($PG_EXPORTER_EXCLUDE_DATABASE)
  -i, --include-database=""  included databases when enabling auto-discovery ($PG_EXPORTER_INCLUDE_DATABASE)
  -n, --namespace=""         prefix of built-in metrics, (pg|pgbouncer) by default ($PG_EXPORTER_NAMESPACE)
  -f, --[no-]fail-fast       fail fast instead of waiting during start-up ($PG_EXPORTER_FAIL_FAST)
  -T, --connect-timeout=100  connect timeout in ms, 100 by default ($PG_EXPORTER_CONNECT_TIMEOUT)
  -P, --web.telemetry-path="/metrics"
                             URL path under which to expose metrics. ($PG_EXPORTER_TELEMETRY_PATH)
  -D, --[no-]dry-run         dry run and print raw configs
  -E, --[no-]explain         explain server planned queries
      --log.level="info"     log level: debug|info|warn|error
      --[no-]version         Show application version.
```




--------------------

## API

Here are `pg_exporter` REST APIs

```bash
# Fetch metrics (metrics path depends on parameters)
curl localhost:9630/metrics

# Reload configuration
curl localhost:9630/reload

# Explain configuration
curl localhost:9630/explain

# Print Statistics
curl localhost:9630/stat

# Aliveness health check (200 up, 503 down)
curl localhost:9630/up
curl localhost:9630/health
curl localhost:9630/liveness
curl localhost:9630/readiness

# traffic route health check

### 200 if not in recovery, 404 if in recovery, 503 if server is down
curl localhost:9630/primary
curl localhost:9630/leader
curl localhost:9630/master
curl localhost:9630/read-write
curl localhost:9630/rw

### 200 if in recovery, 404 if not in recovery, 503 if server is down
curl localhost:9630/replica
curl localhost:9630/standby
curl localhost:9630/slave
curl localhost:9630/read-only
curl localhost:9630/ro

### 200 if server is ready for read traffic (including primary), 503 if server is down
curl localhost:9630/read
```


--------------------

## Build

Build on your local machine:

```bash
go build
```

To build a static stand-alone binary for docker scratch

```bash
CGO_ENABLED=0 GOOS=linux go build -a -ldflags '-extldflags "-static"' -o pg_exporter
```

To build a docker image, use:

```bash
make docker
```

Or [download](https://github.com/Vonng/pg_exporter/releases) the latest prebuilt binaries, rpms, debs from release pages.




--------------------

## Deployment

Redhat rpm and Debian/Ubuntu deb packages is made with `nfpm`.

* `/usr/bin/pg_exporter`: the binary file。
* [`/etc/default/pg_exporter`](package/pg_exporter.default): the envs & options
* [`/etc/pg_exporter.yml`](package/pg_exporter.default): the config file

Which is also available on Pigsty's PGSQL repo.


--------------------

## Configuration

Configs lie in the core of `pg_exporter`. Actually, this project contains more lines of YAML than go.

* A monolith battery-included config file: [`pg_exporter.yml`](package/pg_exporter.yml)
* Separated metrics definition in [`config/collector`](config/collector)
* Example of how to write a config file:  [`doc.yml`](config/collector/000-doc.yml)

Current `pg_exporter` is shipped with the following metrics collector definition files

> #### Note
>
> Supported version: PostgreSQL 10, 11, 12, 13, 14, 15, 16, 17+
>
> But you can still get PostgreSQL 9.4, 9.5, 9.6 support by switching to the older version collector definition

- [`pg`](config/collector/110-pg.yml)
- [`pg_meta`](config/collector/120-pg_meta.yml)
- [`pg_setting`](config/collector/130-pg_setting.yml)
- [`pg_repl`](config/collector/210-pg_repl.yml)
- [`pg_sync_standby`](config/collector/220-pg_sync_standby.yml)
- [`pg_downstrem`](config/collector/230-pg_downstream.yml)
- [`pg_slot`](config/collector/240-pg_slot.yml)
- [`pg_recv`](config/collector/250-pg_recv.yml)
- [`pg_sub`](config/collector/260-pg_sub.yml)
- [`pg_origin`](config/collector/270-pg_origin.yml)
- [`pg_io`](config/collector/300-pg_io.yml)
- [`pg_size`](config/collector/310-pg_size.yml)
- [`pg_archiver`](config/collector/320-pg_archiver.yml)
- [`pg_bgwriter`](config/collector/330-pg_bgwriter.yml)
- [`pg_checkpointer`](config/collector/331-pg_checkpointer.yml)
- [`pg_ssl`](config/collector/340-pg_ssl.yml)
- [`pg_checkpoint`](config/collector/350-pg_checkpoint.yml)
- [`pg_recovery`](config/collector/360-pg_recovery.yml)
- [`pg_slru`](config/collector/370-pg_slru.yml)
- [`pg_shmem`](config/collector/380-pg_shmem.yml)
- [`pg_wal`](config/collector/390-pg_wal.yml)
- [`pg_activity`](config/collector/410-pg_activity.yml)
- [`pg_wait`](config/collector/420-pg_wait.yml)
- [`pg_backend`](config/collector/430-pg_backend.yml)
- [`pg_xact`](config/collector/440-pg_xact.yml)
- [`pg_lock`](config/collector/450-pg_lock.yml)
- [`pg_query`](config/collector/460-pg_query.yml)
- [`pg_vacuuming`](config/collector/510-pg_vacuuming.yml)
- [`pg_indexing`](config/collector/520-pg_indexing.yml)
- [`pg_clustering`](config/collector/530-pg_clustering.yml)
- [`pg_backup`](config/collector/540-pg_backup.yml)
- [`pg_db`](config/collector/610-pg_db.yml)
- [`pg_db_confl`](config/collector/620-pg_db_confl.yml)
- [`pg_pubrel`](config/collector/640-pg_pubrel.yml)
- [`pg_subrel`](config/collector/650-pg_subrel.yml)
- [`pg_table`](config/collector/700-pg_table.yml)
- [`pg_index`](config/collector/710-pg_index.yml)
- [`pg_func`](config/collector/720-pg_func.yml)
- [`pg_seq`](config/collector/730-pg_seq.yml)
- [`pg_relkind`](config/collector/740-pg_relkind.yml)
- [`pg_defpart`](config/collector/750-pg_defpart.yml)
- [`pg_table_size`](config/collector/810-pg_table_size.yml)
- [`pg_table_bloat`](config/collector/820-pg_table_bloat.yml)
- [`pg_index_bloat`](config/collector/830-pg_index_bloat.yml)
- [`pgbouncer_list`](config/collector/910-pgbouncer_list.yml)
- [`pgbouncer_database`](config/collector/920-pgbouncer_database.yml)
- [`pgbouncer_stat`](config/collector/930-pgbouncer_stat.yml)
- [`pgbouncer_pool`](config/collector/940-pgbouncer_pool.yml)

`pg_exporter` will generate approximately 200~300 metrics for a completely new database cluster. For a real-world database with 10 ~ 100 tables, it may generate several 1k ~ 10k metrics. You may need to modify or disable some database-level metrics on a database with several thousand or more tables in order to complete the scrape in time.

Config files are using YAML format, there are lots of examples in the [conf](https://github.com/Vonng/pg_exporter/tree/master/config/collector) dir. and here is a [sample](config/collector/000-doc.yml) config.

```
#==============================================================#
# 1. Config File
#==============================================================#
# The configuration file for pg_exporter is a YAML file.
# Default configuration are retrieved via following precedence:
#     1. command line args:      --config=<config path>
#     2. environment variables:  PG_EXPORTER_CONFIG=<config path>
#     3. pg_exporter.yml        (Current directory)
#     4. /etc/pg_exporter.yml   (config file)
#     5. /etc/pg_exporter       (config dir)

#==============================================================#
# 2. Config Format
#==============================================================#
# pg_exporter config could be a single YAML file, or a directory containing a series of separated YAML files.
# each YAML config file is consist of one or more metrics Collector definition. Which are top-level objects
# If a directory is provided, all YAML in that directory will be merged in alphabetic order.
# Collector definition examples are shown below.

#==============================================================#
# 3. Collector Example
#==============================================================#
#  # Here is an example of a metrics collector definition
#  pg_primary_only:       # Collector branch name. Must be UNIQUE among the entire configuration
#    name: pg             # Collector namespace, used as METRIC PREFIX, set to branch name by default, can be override
#                         # the same namespace may contain multiple collector branches. It`s the user`s responsibility
#                         # to make sure that AT MOST ONE collector is picked for each namespace.
#
#    desc: PostgreSQL basic information (on primary)                 # Collector description
#    query: |                                                        # Metrics Query SQL
#
#      SELECT extract(EPOCH FROM CURRENT_TIMESTAMP)                  AS timestamp,
#             pg_current_wal_lsn() - `0/0`                           AS lsn,
#             pg_current_wal_insert_lsn() - `0/0`                    AS insert_lsn,
#             pg_current_wal_lsn() - `0/0`                           AS write_lsn,
#             pg_current_wal_flush_lsn() - `0/0`                     AS flush_lsn,
#             extract(EPOCH FROM now() - pg_postmaster_start_time()) AS uptime,
#             extract(EPOCH FROM now() - pg_conf_load_time())        AS conf_reload_time,
#             pg_is_in_backup()                                      AS is_in_backup,
#             extract(EPOCH FROM now() - pg_backup_start_time())     AS backup_time;
#
#                             # [OPTIONAL] metadata fields, control collector behavior
#    ttl: 10                  # Cache TTL: in seconds, how long will pg_exporter cache this collector`s query result.
#    timeout: 0.1             # Query Timeout: in seconds, query that exceed this limit will be canceled.
#    min_version: 100000      # minimal supported version, boundary IS included. In server version number format,
#    max_version: 130000      # maximal supported version, boundary NOT included, In server version number format
#    fatal: false             # Collector marked `fatal` fails, the entire scrape will abort immediately and marked as failed
#    skip: false              # Collector marked `skip` will not be installed during the planning procedure
#
#    tags: [cluster, primary] # Collector tags, used for planning and scheduling
#
#    # tags are list of strings, which could be:
#    #   * `cluster` marks this query as cluster level, so it will only execute once for the same PostgreSQL Server
#    #   * `primary` or `master`  mark this query can only run on a primary instance (WILL NOT execute if pg_is_in_recovery())
#    #   * `standby` or `replica` mark this query can only run on a replica instance (WILL execute if pg_is_in_recovery())
#    # some special tag prefix have special interpretation:
#    #   * `dbname:<dbname>` means this query will ONLY be executed on database with name `<dbname>`
#    #   * `username:<user>` means this query will only be executed when connect with user `<user>`
#    #   * `extension:<extname>` means this query will only be executed when extension `<extname>` is installed
#    #   * `schema:<nspname>` means this query will only by executed when schema `<nspname>` exist
#    #   * `not:<negtag>` means this query WILL NOT be executed when exporter is tagged with `<negtag>`
#    #   * `<tag>` means this query WILL be executed when exporter is tagged with `<tag>`
#    #   ( <tag> could not be cluster,primary,standby,master,replica,etc...)
#
#    # One or more "predicate queries" may be defined for a metric query. These
#    # are run before the main metric query (after any cache hit check). If all
#    # of them, when run sequentially, return a single row with a single column
#    # boolean true result, the main metric query is executed. If any of them
#    # return false or return zero rows, the main query is skipped. If any
#    # predicate query returns more than one row, a non-boolean result, or fails
#    # with an error the whole query is marked failed. Predicate queries can be
#    # used to check for the presence of specific functions, tables, extensions,
#    # settings, vendor-specific postgres features etc before running the main query.
#
#    predicate_queries:
#      - name: predicate query name
#        predicate_query: |
#          SELECT EXISTS (SELECT 1 FROM information_schema.routines WHERE routine_schema = 'pg_catalog' AND routine_name = 'pg_backup_start_time');
#
#    metrics:                 # List of returned columns, each column must have a `name` and `usage`, `rename` and `description` are optional
#      - timestamp:           # Column name, should be exactly the same as returned column name
#          usage: GAUGE       # Metric type, `usage` could be
#                                  * DISCARD: completely ignoring this field
#                                  * LABEL:   use columnName=columnValue as a label in metric
#                                  * GAUGE:   Mark column as a gauge metric, full name will be `<query.name>_<column.name>`
#                                  * COUNTER: Same as above, except it is a counter rather than a gauge.
#          rename: ts         # [OPTIONAL] Alias, optional, the alias will be used instead of the column name
#          description: xxxx  # [OPTIONAL] Description of the column, will be used as a metric description
#          default: 0         # [OPTIONAL] Default value, will be used when column is NULL
#          scale:   1000      # [OPTIONAL] Scale the value by this factor
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
#          description: seconds since the current backup start. null if don`t have one
#
#      .... # you can also use rename & scale to customize the metric name and value:
#      - checkpoint_write_time:
#          rename: write_time
#          usage: COUNTER
#          scale: 1e-3
#          description: Total amount of time that has been spent in the portion of checkpoint processing where files are written to disk, in seconds

#==============================================================#
# 4. Collector Presets
#==============================================================#
# pg_exporter is shipped with a series of preset collectors (already numbered and ordered by filename)
#
# 1xx  Basic metrics:        basic info, metadata, settings
# 2xx  Replication metrics:  replication, walreceiver, downstream, sync standby, slots, subscription
# 3xx  Persist metrics:      size, wal, background writer, checkpointer, ssl, checkpoint, recovery, slru cache, shmem usage
# 4xx  Activity metrics:     backend count group by state, wait event, locks, xacts, queries
# 5xx  Progress metrics:     clustering, vacuuming, indexing, basebackup, copy
# 6xx  Database metrics:     pg_database, publication, subscription
# 7xx  Object metrics:       pg_class, table, index, function, sequence, default partition
# 8xx  Optional metrics:     optional metrics collector (disable by default, slow queries)
# 9xx  Pgbouncer metrics:    metrics from pgbouncer admin database `pgbouncer`
#
# 100-599 Metrics for entire database cluster  (scrape once)
# 600-899 Metrics for single database instance (scrape for each database ,except for pg_db itself)

#==============================================================#
# 5. Cache TTL
#==============================================================#
# Cache can be used for reducing query overhead, it can be enabled by setting a non-zero value for `ttl`
# It is highly recommended to use cache to avoid duplicate scrapes. Especially when you got multiple Prometheus
# scraping the same instance with slow monitoring queries. Setting `ttl` to zero or leaving blank will disable
# result caching, which is the default behavior
#
# TTL has to be smaller than your scrape interval. 15s scrape interval and 10s TTL is a good start for
# production environment. Some expensive monitoring queries (such as size/bloat check) will have longer `ttl`
# which can also be used as a mechanism to achieve `different scrape frequency`

#==============================================================#
# 6. Query Timeout
#==============================================================#
# Collectors can be configured with an optional Timeout. If the collector`s query executes more than that
# timeout, it will be canceled immediately. Setting the `timeout` to 0 or leaving blank will reset it to
# default timeout 0.1 (100ms). Setting it to any negative number will disable the query timeout feature.
# All queries have a default timeout of 100ms, if exceeded, the query will be canceled immediately to avoid
# avalanche. You can explicitly overwrite that option. but beware: in some extreme cases, if all your
# timeout sum up greater your scrape/cache interval (usually 15s), the queries may still be jammed.
# or, you can just disable potential slow queries.

#==============================================================#
# 7. Version Compatibility
#==============================================================#
# Each collector has two optional version compatibility parameters: `min_version` and `max_version`.
# These two parameters specify the version compatibility of the collector. If target postgres/pgbouncer
# version is less than `min_version`, or higher than `max_version`, the collector will not be installed.
# These two parameters are using PostgreSQL server version number format, which is a 6-digit integer
# format as <major:2 digit><minor:2 digit>:<release: 2 digit>.
# For example, 090600 stands for 9.6 and 120100 stands for 12.1
# And beware that version compatibility range is left-inclusive right exclusive: [min, max), set to zero or
# leaving blank will affect as -inf or +inf

#==============================================================#
# 8. Fatality
#==============================================================#
# If a collector is marked with `fatal` falls, the entire scrape operation will be marked as fail and key metrics
# `pg_up` / `pgbouncer_up` will be reset to 0. It is always a good practice to set up AT LEAST ONE fatal
# collector for pg_exporter. `pg.pg_primary_only` and `pgbouncer_list` are the default fatal collector.
#
# If a collector without `fatal` flag fails, it will increase global fail counters. But the scrape operation
# will carry on. The entire scrape result will not be marked as faile, thus will not affect the `<xx>_up` metric.

#==============================================================#
# 9. Skip
#==============================================================#
# Collector with `skip` flag set to true will NOT be installed.
# This could be a handy option to disable collectors

#==============================================================#
# 10. Tags and Planning
#==============================================================#
# Tags are designed for collector planning & schedule. It can be handy to customize which queries run
# on which instances. And thus you can use one-single monolith config for multiple environments
#
#  Tags are a list of strings, each string could be:
#  Pre-defined special tags
#    * `cluster` marks this collector as cluster level, so it will ONLY BE EXECUTED ONCE for the same PostgreSQL Server
#    * `primary` or `master` mark this collector as primary-only, so it WILL NOT work iff pg_is_in_recovery()
#    * `standby` or `replica` mark this collector as replica-only, so it WILL work iff pg_is_in_recovery()
#  Special tag prefix which have different interpretation:
#    * `dbname:<dbname>` means this collector will ONLY work on database with name `<dbname>`
#    * `username:<user>` means this collector will ONLY work when connect with user `<user>`
#    * `extension:<extname>` means this collector will ONLY work when extension `<extname>` is installed
#    * `schema:<nspname>` means this collector will only work when schema `<nspname>` exists
#  Customized positive tags (filter) and negative tags (taint)
#    * `not:<negtag>` means this collector WILL NOT work when exporter is tagged with `<negtag>`
#    * `<tag>` means this query WILL work if exporter is tagged with `<tag>` (special tags not included)
#
#  pg_exporter will trigger the Planning procedure after connecting to the target. It will gather database facts
#  and match them with tags and other metadata (such as supported version range). Collector will only
#  be installed if and only if it is compatible with the target server.
```



--------------------

## About

Author: [Vonng](https://vonng.com/en) ([rh@vonng.com](mailto:rh@vonng.com))

Contributors: https://github.com/Vonng/pg_exporter/graphs/contributors

License: [Apache License Version 2.0](LICENSE)

Copyright: 2018-2025 rh@vonng.com
