/***********************************************************************\
Copyright © 2021 Ruohang Feng <rh@vonng.com>

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
\***********************************************************************/
package exporter

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/log"
	"sort"
	"strings"
	"sync"
	"time"
)

/**********************************************************************************************\
*                                       Server                                                 *
\**********************************************************************************************/

// Server represent a postgres connection, with additional fact, conf, runtime info
type Server struct {
	*sql.DB              // database instance (do not close this due to the stupid implementation in database/sql)
	dsn     string       // data source name
	lock    sync.RWMutex // server scrape lock
	err     error        // last error

	// hooks
	beforeScrape     func(s *Server) error        // hook: execute before scrape
	onDatabaseChange func(change map[string]bool) // hook: invoke when database list is changed

	// postgres fact gather from server
	UP         bool            // indicate whether target server is connectable
	Recovery   bool            // is server in recovering
	Version    int             // pg server version num
	Database   string          // database name of current server connection
	Username   string          // current username
	Databases  map[string]bool // all available database in target cluster
	dblistLock sync.Mutex      // lock when access Databases map
	Namespaces map[string]bool // all available schema in target cluster
	Extensions map[string]bool // all available extension in target cluster

	Tags           []string // server tags set by cli arg --tag
	PgbouncerMode  bool     // indicate it is a pgbouncer server
	DisableCache   bool     // force executing, ignoring caching policy
	ExcludeDbnames []string // if ExcludeDbnames is provided, Auto Database Discovery is enabled
	Forked         bool     // is this a forked server ? (does not run cluster level query)
	Planned        bool     // if false, server will trigger a plan before collect

	// query
	instances []*QueryInstance  // query instance
	queries   map[string]*Query // queries map, keys are config file top layer key
	labels    prometheus.Labels // constant labels

	// internal stats
	serverInit  time.Time // server init timestamp
	scrapeBegin time.Time // server last scrape begin time
	scrapeDone  time.Time // server last scrape done time
	errorCount  float64   // total error count on this server
	totalCount  float64   // total scrape count on this server
	totalTime   float64   // total time spend on scraping

	queryCacheTTL          map[string]float64 // internal query metrics: cache time to live
	queryScrapeTotalCount  map[string]float64 // internal query metrics: total executed
	queryScrapeHitCount    map[string]float64 // internal query metrics: times serving from hit cache
	queryScrapeErrorCount  map[string]float64 // internal query metrics: times failed
	queryScrapeMetricCount map[string]float64 // internal query metrics: number of metrics scrapped
	queryScrapeDuration    map[string]float64 // internal query metrics: time spend on executing
}

// Name is coalesce(s.Database, dsn)
func (s *Server) Name() string {
	if s.Database != "" {
		return s.Database
	}
	return shadowDSN(s.dsn)
}

// Name is coalesce(s.Database, dsn)
func (s *Server) Error() error {
	return s.err
}

// Check will issue a connection and executing precheck hook function
func (s *Server) Check() error {
	return s.beforeScrape(s)
}

// PgbouncerPrecheck checks pgbouncer connection before scrape
func PgbouncerPrecheck(s *Server) (err error) {
	if s.DB == nil { // if db is not initialized, create a new DB
		if s.DB, err = sql.Open("postgres", s.dsn); err != nil {
			s.UP = false
			return
		}
		s.DB.SetMaxIdleConns(1)
		s.DB.SetMaxOpenConns(1)
		s.DB.SetConnMaxLifetime(60 * time.Second)
	}

	if _, err = s.DB.Exec(`SHOW VERSION;`); err != nil {
		// TODO: since pgbouncer 1.12- using NOTICE to tell version, we just leave it blank here
		return nil
	}
	return nil
}

// PostgresPrecheck checks postgres connection and gathering facts
// if any important fact changed, it will triggers a plan before next scrape
func PostgresPrecheck(s *Server) (err error) {
	if s.DB == nil { // if db is not initialized, create a new DB
		if s.DB, err = sql.Open("postgres", s.dsn); err != nil {
			s.UP = false
			return
		}
		s.DB.SetMaxIdleConns(1)
		s.DB.SetMaxOpenConns(1)
		s.DB.SetConnMaxLifetime(120 * time.Second)
	}

	// retrieve version info
	var version int
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err = s.DB.QueryRowContext(ctx, `SHOW server_version_num;`).Scan(&version); err != nil {
		s.UP = false
		return fmt.Errorf("fail fetching server version: %w", err)
	}
	s.UP = true
	// fact change triggers a new planning
	if s.Version != version {
		log.Infof("server [%s] version changed: from [%d] to [%d]", s.Name(), s.Version, version)
		s.Planned = false
	}
	s.Version = version

	// do not check here
	if _, err = s.DB.Exec(`SET application_name = pg_exporter;`); err != nil {
		s.UP = false
		return fmt.Errorf("fail settting application name: %w", err)
	}

	// get important metadata
	var recovery bool
	var datname, username string
	var databases, namespaces, extensions []string
	precheckSQL := `SELECT current_catalog, current_user, pg_is_in_recovery(),       
	(SELECT array_agg(datname) AS databases FROM pg_database),
	(SELECT array_agg(nspname) AS namespaces FROM pg_namespace),
	(SELECT array_agg(extname) AS extensions FROM pg_extension);`
	ctx, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	if err = s.DB.QueryRowContext(ctx, precheckSQL).Scan(&datname, &username, &recovery, pq.Array(&databases), pq.Array(&namespaces), pq.Array(&extensions)); err != nil {
		s.UP = false
		return fmt.Errorf("fail fetching server version: %w", err)
	}
	if s.Recovery != recovery {
		log.Infof("server [%s] recovery status changed: from [%v] to [%v]", s.Name(), s.Recovery, recovery)
		s.Planned = false
	}
	s.Recovery = recovery
	s.Username = username
	if s.Database != datname {
		log.Infof("server [%s] datname changed: from [%s] to [%s]", s.Name(), s.Database, datname)
		s.Planned = false
	}
	s.Database = datname
	s.Databases[datname] = true

	// update schema & extension list
	s.Namespaces = make(map[string]bool, len(namespaces))
	for _, nsname := range namespaces {
		s.Namespaces[nsname] = true
	}
	s.Extensions = make(map[string]bool, len(extensions))
	for _, extname := range extensions {
		s.Extensions[extname] = true
	}

	// detect db change
	s.dblistLock.Lock()
	defer s.dblistLock.Unlock()
	newDBList := make(map[string]bool, len(databases))
	changes := make(map[string]bool)
	// if new db is not found in old db list, add a change entry [NewDBName:true]
	for _, dbname := range databases {
		newDBList[dbname] = true
		if _, found := s.Databases[dbname]; !found {
			log.Debugf("server [%s] found new database %s", s.Name(), dbname)
			changes[dbname] = true
		}
	}
	// if old db is not found in new db list, add a change entry [OldDBName:false]
	for dbname := range s.Databases {
		if _, found := newDBList[dbname]; !found {
			log.Debugf("server [%s] found vanished database %s", s.Name(), dbname)
			changes[dbname] = false
		}
	}
	// invoke hook if there are changes on database list
	if len(changes) > 0 && s.onDatabaseChange != nil {
		log.Debugf("server [%s] auto discovery database list change : %v", s.Name(), changes)
		s.onDatabaseChange(changes) // if doing something long, launch another goroutine
	}
	s.Databases = newDBList
	return nil
}

// Plan will install queries that compatible with server fact (version, level, recovery, plugin, tags,...)
func (s *Server) Plan(queries ...*Query) {
	// if queries are explicitly given, use it instead of server.queries
	if len(queries) > 0 {
		newQueries := make(map[string]*Query, 0)
		for _, q := range queries {
			newQueries[q.Name] = q
		}
		s.queries = newQueries
	}

	// check query compatibility
	instances := make([]*QueryInstance, 0)
	var installedNames, discardedNames []string
	for name, query := range s.queries {
		if ok, reason := s.Compatible(query); ok {
			instances = append(instances, NewQueryInstance(query, s))
			installedNames = append(installedNames, name)
		} else {
			discardedNames = append(discardedNames, name)
			log.Debugf("query [%s].%s discarded because of %s", query.Name, name, reason)
		}
	}

	// sort by priority
	sort.Slice(instances, func(i, j int) bool {
		return instances[i].Priority < instances[j].Priority
	})
	s.instances = instances

	// reset statistics after planning
	s.ResetStats()
	s.Planned = true
	log.Infof("server [%s] planned with %d queries, %d installed, %d discarded, installed: %s , discarded: %s",
		s.Name(), len(s.queries), len(installedNames), len(discardedNames), strings.Join(installedNames, ", "), strings.Join(discardedNames, ", "))
}

// ResetStats will clear all statistic info
func (s *Server) ResetStats() {
	s.queryCacheTTL = make(map[string]float64, 0)
	s.queryScrapeTotalCount = make(map[string]float64, 0)
	s.queryScrapeHitCount = make(map[string]float64, 0)
	s.queryScrapeErrorCount = make(map[string]float64, 0)
	s.queryScrapeMetricCount = make(map[string]float64, 0)
	s.queryScrapeDuration = make(map[string]float64, 0)

	for _, query := range s.instances {
		s.queryCacheTTL[query.Name] = 0
		s.queryScrapeTotalCount[query.Name] = 0
		s.queryScrapeHitCount[query.Name] = 0
		s.queryScrapeErrorCount[query.Name] = 0
		s.queryScrapeMetricCount[query.Name] = 0
		s.queryScrapeDuration[query.Name] = 0
	}
}

// Compatible tells whether a query is compatible with current server
func (s *Server) Compatible(query *Query) (res bool, reason string) {
	// check skip flag
	if query.Skip {
		return false, fmt.Sprintf("query %s is marked skip", query.Name)
	}

	// check mode
	if pgbouncerQuery := query.HasTag("pgbouncer"); pgbouncerQuery != s.PgbouncerMode {
		if s.PgbouncerMode {
			return false, fmt.Sprintf("pgbouncer server doese not match with normal postgres query %s", query.Name)
		}
		return false, fmt.Sprintf("pgbouncer query %s does not match with normal postgres server", query.Name)
	}

	// check version
	if s.Version != 0 { // if version is not determined yet, just let it go
		if query.MinVersion != 0 && s.Version < query.MinVersion {
			return false, fmt.Sprintf("server version %v lower than query min version %v", s.Version, query.MinVersion)
		}
		if query.MaxVersion != 0 && s.Version >= query.MaxVersion { // exclude
			return false, fmt.Sprintf("server version %v higher than query max version %v", s.Version, query.MaxVersion)
		}
	}

	// check query side tags
	for _, tag := range query.Tags {
		// check extension is installed on target database
		if strings.HasPrefix(tag, "extension:") {
			if _, found := s.Extensions[strings.TrimPrefix(tag, "extension:")]; !found {
				return false, fmt.Sprintf("server [%s] does not have extension %s", s.Name(), tag)
			}
			continue
		}

		// check schema exist on target database
		if strings.HasPrefix(tag, "schema:") {
			if _, found := s.Namespaces[strings.TrimPrefix(tag, "schema:")]; !found {
				return false, fmt.Sprintf("server [%s] does not have schema %s", s.Name(), tag)
			}
			continue
		}

		// check if dbname prefix tag match server.Database
		if strings.HasPrefix(tag, "dbname:") {
			if s.Database != strings.TrimPrefix(tag, "dbname:") {
				return false, fmt.Sprintf("server [%s] dbname does %s not match with query tag %s", s.Name(), s.Database, tag)
			}
			continue
		}

		// check if username prefix tag match server.Username
		if strings.HasPrefix(tag, "username:") {
			if s.Username != strings.TrimPrefix(tag, "username:") {
				return false, fmt.Sprintf("server [%s] username [%s] does not match %s", s.Name(), s.Username, tag)
			}
			continue
		}

		// check server does not have given tag
		if strings.HasPrefix(tag, "not:") {
			if negTag := strings.TrimPrefix(tag, "not:"); s.HasTag(negTag) {
				return false, fmt.Sprintf("server [%s] has tag %s that query %s forbid", s.Name(), negTag, query.Name)
			}
			continue
		}

		// check 3 default tags: cluster, primary, standby|replica
		switch tag {
		case "cluster":
			if s.Forked {
				return false, fmt.Sprintf("cluster level query %s will not run on forked server %v", query.Name, s.Name())
			}
			continue
		case "primary", "master", "leader":
			if s.Recovery {
				return false, fmt.Sprintf("primary-only query %s will not run on standby server %v", query.Name, s.Name())
			}
			continue
		case "standby", "replica", "slave":
			if !s.Recovery {
				return false, fmt.Sprintf("standby-only query %s will not run on primary server %v", query.Name, s.Name())
			}
			continue
		case "pgbouncer":
			continue
		default:
			// if this tag is nether a pre-defined tag nor a prefixed pattern tag, check whether server have that tag
			if !s.HasTag(tag) {
				return false, fmt.Sprintf("server [%s] does not have tag %s that query %s require", s.Name(), tag, query.Name)
			}
		}
	}
	return true, ""
}

// Explain will print all queries that registered to server
func (s *Server) Explain() (res []string) {
	for _, i := range s.instances {
		res = append(res, i.Explain())
	}
	return
}

// Describe implement prometheus.Collector
func (s *Server) Describe(ch chan<- *prometheus.Desc) {
	for _, instance := range s.instances {
		instance.Describe(ch)
	}
}

// Collect implement prometheus.Collector interface
func (s *Server) Collect(ch chan<- prometheus.Metric) {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.scrapeBegin = time.Now() // This ts is used for cache expiration check

	// check server conn, gathering fact
	if s.err = s.Check(); s.err != nil {
		log.Debugf("fail establishing connection to %s: %s", s.Name(), s.err.Error())
		goto final
	}

	// fact change (including first time) will incur a plan procedure
	if !s.Planned {
		s.Plan()
	}

	for _, query := range s.instances {
		query.Collect(ch)
		s.queryCacheTTL[query.Name] = query.cacheTTL()
		s.queryScrapeTotalCount[query.Name]++
		s.queryScrapeMetricCount[query.Name] = float64(query.ResultSize())
		s.queryScrapeDuration[query.Name] = query.scrapeDuration.Seconds() // use the real exec as duration
		if query.Error() != nil {
			s.queryScrapeErrorCount[query.Name]++
			if query.Fatal { // treat as fatal error
				log.Errorf("query [%s] error: %s", query.Name, query.Error())
				s.err = query.Error()
				goto final
			} else { // skip this error according to config
				log.Warnf("query [%s] error skipped: %s", query.Name, query.Error())
				continue
			}
		} else {
			if query.CacheHit() {
				s.queryScrapeHitCount[query.Name]++
			}
		}
	}

final:
	s.scrapeDone = time.Now() // This ts is used for cache expiration check
	s.totalTime += s.scrapeDone.Sub(s.scrapeBegin).Seconds()
	s.totalCount++
	if s.err != nil {
		s.UP = false
		s.errorCount++
		log.Errorf("fail scrapping server [%s]: %s", s.Name(), s.err.Error())
	} else {
		s.UP = true
		log.Debugf("server [%s] scrapped in %v",
			s.Name(), s.scrapeDone.Sub(s.scrapeBegin).Seconds())
	}
}

// HasTag tells whether this server have specific tag
func (s *Server) HasTag(tag string) bool {
	for _, t := range s.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// Duration returns last scrape duration in float64 seconds
func (s *Server) Duration() float64 {
	s.lock.RLock()
	s.lock.RUnlock()
	return s.scrapeDone.Sub(s.scrapeBegin).Seconds()
}

// Uptime returns servers's uptime
func (s *Server) Uptime() float64 {
	return time.Now().Sub(s.serverInit).Seconds()
}

/**************************************************************\
* Server Creation
\**************************************************************/

// NewServer will check dsn, but not trying to connect
func NewServer(dsn string, opts ...ServerOpt) *Server {
	s := &Server{dsn: dsn}
	for _, opt := range opts {
		opt(s)
	}
	s.Database = parseDatname(dsn)
	if s.Database != "pgbouncer" {
		s.PgbouncerMode = false
		s.beforeScrape = PostgresPrecheck
	} else {
		log.Infof("datname pgbouncer detected, enabling pgbouncer mode")
		s.PgbouncerMode = true
		s.beforeScrape = PgbouncerPrecheck
	}
	s.Databases = make(map[string]bool, 1)
	s.serverInit = time.Now()
	return s
}

// ServerOpt configures Server
type ServerOpt func(*Server)

// WithConstLabel copy constant label map to server
func WithConstLabel(labels prometheus.Labels) ServerOpt {
	return func(s *Server) {
		if labels == nil {
			s.labels = nil
		} else {
			s.labels = make(prometheus.Labels, len(labels))
			for k, v := range labels {
				s.labels[k] = v
			}
		}
	}
}

// WithCachePolicy will pass cache option to server
func WithCachePolicy(disableCache bool) ServerOpt {
	return func(s *Server) {
		s.DisableCache = disableCache
	}
}

// WithQueries set server's default query set
func WithQueries(queries map[string]*Query) ServerOpt {
	return func(s *Server) {
		s.queries = queries
	}
}

// WithClusterQueryDisabled will marks server only execute query without cluster tag
func WithServerTags(tags []string) ServerOpt {
	return func(s *Server) {
		s.Tags = tags
	}
}
