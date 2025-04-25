package exporter

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
)

/* ================ Const ================ */
const connMaxLifeTime = 1 * time.Minute // close connection after 1 minute to avoid conn leak

/* ================ Server ================ */

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
	UP       bool   // indicate whether target server is connectable
	Recovery bool   // is server in recovering
	Version  int    // pg server version num
	Database string // database name of current server connection
	Username string // current username

	Databases  map[string]bool // all available database in target cluster
	dblistLock sync.Mutex      // lock when access Databases map

	Namespaces map[string]bool // all available schema in target cluster
	Extensions map[string]bool // all available extension in target cluster

	Tags            []string // server tags set by cli arg --tag
	PgbouncerMode   bool     // indicate it is a pgbouncer server
	DisableCache    bool     // force executing, ignoring caching policy
	ExcludeDbnames  []string // if ExcludeDbnames is provided, Auto Database Discovery is enabled
	Forked          bool     // is this a forked server ? (does not run cluster level query)
	Planned         bool     // if false, server will trigger a plan before collect
	MaxConn         int      // max connection for this server
	ConnectTimeout  int      // connect timeout for this server in ms
	ConnMaxLifetime int      // connection max lifetime for this server in seconds

	// query
	Collectors []*Collector      // query collector instance (installed query)
	queries    map[string]*Query // queries map, keys are config file top layer key
	labels     prometheus.Labels // constant labels

	// internal stats
	serverInit  time.Time // server init timestamp
	scrapeBegin time.Time // server last scrape begin time
	scrapeDone  time.Time // server last scrape done time
	errorCount  float64   // total error count on this server
	totalCount  float64   // total scrape count on this server
	totalTime   float64   // total time spend on scraping

	queryCacheTTL                 map[string]float64 // internal query metrics: cache time to live
	queryScrapeTotalCount         map[string]float64 // internal query metrics: total executed
	queryScrapeHitCount           map[string]float64 // internal query metrics: times serving from hit cache
	queryScrapeErrorCount         map[string]float64 // internal query metrics: times failed
	queryScrapePredicateSkipCount map[string]float64 // internal query metrics: times skipped due to predicate
	queryScrapeMetricCount        map[string]float64 // internal query metrics: number of metrics scraped
	queryScrapeDuration           map[string]float64 // internal query metrics: time spend on executing
}

func (s *Server) GetConnectTimeout() time.Duration {
	if s.ConnectTimeout <= 0 {
		return 100 * time.Millisecond
	}
	return time.Duration(s.ConnectTimeout) * time.Millisecond
}

// Name is coalesce(s.Database, dsn)
func (s *Server) Name() string {
	if s.Database != "" {
		return s.Database
	}
	return ShadowPGURL(s.dsn)
}

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
		s.DB.SetConnMaxLifetime(connMaxLifeTime)
	}

	var version string
	ctx, cancel := context.WithTimeout(context.Background(), s.GetConnectTimeout())
	defer cancel()
	if err = s.DB.QueryRowContext(ctx, `SHOW VERSION;`).Scan(&version); err != nil {
		// TODO: since pgbouncer 1.12- using NOTICE to tell version, we just leave it blank here
		logWarnf("server [%s] fail to get pgbouncer version", s.Name())
		// return fmt.Errorf("fail fetching pgbouncer server version: %w", err)
	} else {
		s.Version = ParseSemver(version)
		if s.Version != 0 {
			logDebugf("server [%s] parse pgbouncer version from %s to %v", s.Name(), version, s.Version)
		} else {
			logWarnf("server [%s] fail to parse pgbouncer version from %v", s.Name(), version)
		}
	}
	return nil
}

// ParseSemver will turn semantic version string into integer
func ParseSemver(semverStr string) int {
	semverRe := regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)
	semver := semverRe.FindStringSubmatch(semverStr)
	logDebugf("parse pgbouncer semver string %s", semverStr)
	if len(semver) != 4 {
		return 0
	}
	verNum := 0
	if major, err := strconv.Atoi(semver[1]); err != nil {
		return 0
	} else {
		verNum += major * 10000
	}
	if minor, err := strconv.Atoi(semver[2]); err != nil {
		return 0
	} else {
		verNum += minor * 100
	}
	if release, err := strconv.Atoi(semver[3]); err != nil {
		return 0
	} else {
		verNum += release
	}
	return verNum
}

// PostgresPrecheck checks postgres connection and gathering facts
// if any important fact changed, it will trigger a plan before next scrape
func PostgresPrecheck(s *Server) (err error) {
	if s.DB == nil { // if db is not initialized, create a new DB
		if s.DB, err = sql.Open("postgres", s.dsn); err != nil {
			s.UP = false
			return
		}
		if s.Forked {
			s.MaxConn = 1
			s.DB.SetMaxIdleConns(1)
			s.DB.SetMaxOpenConns(1)
			s.DB.SetConnMaxLifetime(connMaxLifeTime)
		} else {
			s.MaxConn = 3
			s.DB.SetMaxIdleConns(3)
			s.DB.SetMaxOpenConns(3)
			s.DB.SetConnMaxLifetime(1 * time.Minute)
		}
	}

	// retrieve version info
	var version int
	ctx, cancel := context.WithTimeout(context.Background(), s.GetConnectTimeout())
	defer cancel()
	if err = s.DB.QueryRowContext(ctx, `SHOW server_version_num;`).Scan(&version); err != nil {
		s.UP = false
		return fmt.Errorf("fail fetching server version: %w", err)
	}
	s.UP = true
	// fact change triggers a new planning
	if s.Version != version {
		logInfof("server [%s] version changed: from [%d] to [%d]", s.Name(), s.Version, version)
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
	precheckSQL := `SELECT current_catalog, current_user, pg_catalog.pg_is_in_recovery(),
	(SELECT pg_catalog.array_agg(d.datname)::text[] AS databases FROM pg_catalog.pg_database d WHERE d.datallowconn AND NOT d.datistemplate),
	(SELECT pg_catalog.array_agg(n.nspname)::text[] AS namespaces FROM pg_catalog.pg_namespace n),
	(SELECT pg_catalog.array_agg(e.extname)::text[] AS extensions FROM pg_catalog.pg_extension e);`
	ctx, cancel2 := context.WithTimeout(context.Background(), s.GetConnectTimeout())
	defer cancel2()
	if err = s.DB.QueryRowContext(ctx, precheckSQL).Scan(&datname, &username, &recovery, pq.Array(&databases), pq.Array(&namespaces), pq.Array(&extensions)); err != nil {
		s.UP = false
		return fmt.Errorf("fail fetching server version: %w", err)
	}
	if s.Recovery != recovery {
		logInfof("server [%s] recovery status changed: from [%v] to [%v]", s.Name(), s.Recovery, recovery)
		s.Planned = false
	}
	s.Recovery = recovery
	s.Username = username
	if s.Database != datname {
		logInfof("server [%s] datname changed: from [%s] to [%s]", s.Name(), s.Database, datname)
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
			logDebugf("server [%s] found new database %s", s.Name(), dbname)
			changes[dbname] = true
		}
	}
	// if old db is not found in new db list, add a change entry [OldDBName:false]
	for dbname := range s.Databases {
		if _, found := newDBList[dbname]; !found {
			logDebugf("server [%s] found vanished database %s", s.Name(), dbname)
			changes[dbname] = false
		}
	}
	// invoke hook if there are changes on database list
	if len(changes) > 0 && s.onDatabaseChange != nil {
		logDebugf("server [%s] auto discovery database list change : %v", s.Name(), changes)
		s.onDatabaseChange(changes) // if doing something long, launch another goroutine
	}
	s.Databases = newDBList
	return nil
}

// Plan will install queries that compatible with server fact (version, level, recovery, plugin, tags,...)
func (s *Server) Plan(queries ...*Query) {
	// if queries are explicitly given, use it instead of server.queries
	if len(queries) > 0 {
		newQueries := make(map[string]*Query)
		for _, q := range queries {
			newQueries[q.Name] = q
		}
		s.queries = newQueries
	}

	// check query compatibility
	instances := make([]*Collector, 0)
	var installedNames, discardedNames []string
	for name, query := range s.queries {
		if ok, reason := s.Compatible(query); ok {
			instances = append(instances, NewCollector(query, s))
			installedNames = append(installedNames, query.Branch)
		} else {
			discardedNames = append(discardedNames, query.Branch)
			logDebugf("query [%s].%s discarded because of %s", query.Name, name, reason)
		}
	}

	// sort by priority
	sort.Slice(instances, func(i, j int) bool {
		return instances[i].Priority < instances[j].Priority
	})
	s.Collectors = instances

	// reset statistics after planning
	s.ResetStats()
	s.Planned = true
	logInfof("server [%s] planned with %d queries, %d installed, %d discarded, installed: %s , discarded: %s",
		s.Name(), len(s.queries), len(installedNames), len(discardedNames), strings.Join(installedNames, ", "), strings.Join(discardedNames, ", "))
}

// ResetStats will clear all statistic info
func (s *Server) ResetStats() {
	s.queryCacheTTL = make(map[string]float64, 0)
	s.queryScrapeTotalCount = make(map[string]float64, 0)
	s.queryScrapeHitCount = make(map[string]float64, 0)
	s.queryScrapeErrorCount = make(map[string]float64, 0)
	s.queryScrapePredicateSkipCount = make(map[string]float64, 0)
	s.queryScrapeMetricCount = make(map[string]float64, 0)
	s.queryScrapeDuration = make(map[string]float64, 0)

	for _, query := range s.Collectors {
		s.queryCacheTTL[query.Name] = 0
		s.queryScrapeTotalCount[query.Name] = 0
		s.queryScrapeHitCount[query.Name] = 0
		s.queryScrapeErrorCount[query.Name] = 0
		if len(query.PredicateQueries) > 0 {
			s.queryScrapePredicateSkipCount[query.Name] = 0
		}
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
func (s *Server) Explain() string {
	var res []string
	for _, i := range s.Collectors {
		res = append(res, i.Explain())
	}
	return strings.Join(res, "\n")
}

// Stat will turn Server internal stats into HTML
func (s *Server) Stat() string {
	buf := new(bytes.Buffer)
	//err := statsTemplate.Execute(buf, s)
	//if err != nil {
	//	logErrorf("fail to generate server stats html")
	//	return fmt.Sprintf("fail to generate server stat html, %s", err.Error())
	//}
	buf.WriteString(fmt.Sprintf("%-24s %-10s %-10s %-10s %-10s %-10s %-6s %-10s\n", "name", "total", "hit", "error", "skip", "metric", "ttl/s", "duration/ms"))
	for _, query := range s.Collectors {
		buf.WriteString(fmt.Sprintf("%-24s %-10d %-10d %-10d %-10d %-10d %-6d %-10f\n",
			query.Name,
			int(s.queryScrapeTotalCount[query.Name]),
			int(s.queryScrapeHitCount[query.Name]),
			int(s.queryScrapeErrorCount[query.Name]),
			int(s.queryScrapePredicateSkipCount[query.Name]),
			int(s.queryScrapeMetricCount[query.Name]),
			int(s.queryCacheTTL[query.Name]),
			s.queryScrapeDuration[query.Name]*1000,
		))
	}
	return buf.String()
}

// ExplainHTML will print server stats in HTML format
func (s *Server) ExplainHTML() string {
	var res []string
	for _, i := range s.Collectors {
		res = append(res, i.HTML())
	}
	return strings.Join(res, "</br></br>")
}

// Describe implement prometheus.Collector
func (s *Server) Describe(ch chan<- *prometheus.Desc) {
	for _, instance := range s.Collectors {
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
		logDebugf("fail establishing connection to %s: %s", s.Name(), s.err.Error())
		goto final
	}

	// fact change (including first time) will incur a plan procedure
	if !s.Planned {
		s.Plan()
	}

	// First pass: execute all queries with Fatal flag
	if err := s.collectFatalQueries(ch); err != nil {
		s.err = err
		goto final
	}

	// Second pass: execute remaining non-Fatal queries
	s.collectNonFatalQueries(ch)

final:
	s.scrapeDone = time.Now() // This ts is used for cache expiration check
	s.totalTime += s.scrapeDone.Sub(s.scrapeBegin).Seconds()
	s.totalCount++
	if s.err != nil {
		s.UP = false
		s.errorCount++
		logErrorf("fail scraping server [%s]: %s", s.Name(), s.err.Error())
	} else {
		s.UP = true
		logDebugf("server [%s] scraped in %v",
			s.Name(), s.scrapeDone.Sub(s.scrapeBegin).Seconds())
	}
}

// collectFatalQueries executes all queries with Fatal flag and returns on first error
func (s *Server) collectFatalQueries(ch chan<- prometheus.Metric) error {
	for _, query := range s.Collectors {
		if !query.Fatal {
			continue
		}

		if err := s.executeQuery(query, ch); err != nil {
			logErrorf("query [%s] error: %s", query.Name, err)
			return err
		}
	}
	return nil
}

// collectNonFatalQueries executes all non-Fatal queries and logs errors without stopping
func (s *Server) collectNonFatalQueries(ch chan<- prometheus.Metric) {
	for _, query := range s.Collectors {
		if query.Fatal {
			continue
		}

		if err := s.executeQuery(query, ch); err != nil {
			logWarnf("query [%s] error skipped: %s", query.Name, err)
		}
	}
}

// executeQuery runs a single query and updates its metrics
func (s *Server) executeQuery(query *Collector, ch chan<- prometheus.Metric) error {
	query.Collect(ch)
	s.queryCacheTTL[query.Name] = query.cacheTTL()
	s.queryScrapeTotalCount[query.Name]++
	s.queryScrapeMetricCount[query.Name] = float64(query.ResultSize())
	s.queryScrapeDuration[query.Name] = query.scrapeDuration.Seconds()

	if query.Error() != nil {
		s.queryScrapeErrorCount[query.Name]++
		return query.Error()
	}

	if query.CacheHit() {
		s.queryScrapeHitCount[query.Name]++
	}

	// Update predicate skip count if applicable
	if len(query.PredicateQueries) > 0 {
		skipped, _ := query.PredicateSkip()
		if skipped {
			s.queryScrapePredicateSkipCount[query.Name]++
		} else {
			s.queryScrapePredicateSkipCount[query.Name] = 0
		}
	}

	return nil
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
	defer s.lock.RUnlock()
	sec := s.scrapeDone.Sub(s.scrapeBegin).Seconds()
	return sec
}

// Uptime returns servers's uptime
func (s *Server) Uptime() float64 {
	return time.Since(s.serverInit).Seconds()
}

/* ================ Server Creation ================ */

// NewServer will check dsn, but not trying to connect
func NewServer(dsn string, opts ...ServerOpt) *Server {
	s := &Server{dsn: dsn}
	for _, opt := range opts {
		opt(s)
	}
	s.Database = ParseDatname(dsn)
	if s.Database != "pgbouncer" {
		s.PgbouncerMode = false
		s.beforeScrape = PostgresPrecheck
	} else {
		logInfof("datname pgbouncer detected, enabling pgbouncer mode")
		s.PgbouncerMode = true
		s.beforeScrape = PgbouncerPrecheck
	}
	s.MaxConn = 1
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

// WithServerTags will mark server only execute query without cluster tag
func WithServerTags(tags []string) ServerOpt {
	return func(s *Server) {
		s.Tags = tags
	}
}

// WithServerConnectTimeout will set a connect timeout for server precheck queries
// otherwise, a default value 100ms will be used.
// Increase this value if you are monitoring a remote (cross-DC, cross-AZ) instance
func WithServerConnectTimeout(timeout int) ServerOpt {
	return func(s *Server) {
		s.ConnectTimeout = timeout
	}
}
