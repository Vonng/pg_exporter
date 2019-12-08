package main

import (
	"fmt"
	"io/ioutil"
	"math"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"database/sql"

	_ "github.com/lib/pq"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/log"
	"gopkg.in/alecthomas/kingpin.v2"
	"gopkg.in/yaml.v2"
)

/**********************************************************************************************\
*                                       Parameters                                             *
\**********************************************************************************************/

// Version 0.0.1
var Version = "0.0.1"

var (
	// prometheus http
	listenAddress = kingpin.Flag("web.listen-address", "prometheus web server listen address").Default(":8848").Envar("PG_EXPORTER_LISTEN_ADDRESS").String()
	metricPath    = kingpin.Flag("web.telemetry-path", "Path under which to expose metrics.").Default("/metrics").Envar("PG_EXPORTER_TELEMETRY_PATH").String()
	logLevel      = kingpin.Flag("log-level", "log-level").Default("Info").Envar("PG_EXPORTER_LOG_LEVEL").String()

	// exporter settings
	dataSourceName = kingpin.Flag("dsn", "postgres connect string").Default("postgres://:5432/postgres?host=/tmp&sslmode=disable").Envar("PG_EXPORTER_DSN").String()
	configPath     = kingpin.Flag("config", "Path to config files").Default("pg_exporter.yaml").Envar("PG_EXPORTER_CONFIG_PATH").String()
	constLabels    = kingpin.Flag("label", "Comma separated list label=value pair").Default("").Envar("PG_EXPORTER_CONST_LABELS").String()
	disableCache   = kingpin.Flag("disable-cache", "force not using cache").Default("false").Envar("PG_EXPORTER_DISABLE_CACHE").Bool()

	// action
	dumpMetrics   = kingpin.Flag("explain", "dry run and explain queries").Default("false").Bool()
	tryConnection = kingpin.Flag("try", "try given dsn is connectable").Default("false").Bool()

	// database auto discovery
	//searchDatabases  = kingpin.Flag("search-database", "Set this to enable auto database discovery").Default("false").Envar("PG_EXPORTER_SEARCH_DATABASES").Bool()
	//excludeDatabases = kingpin.Flag("exclude-databases", "Comma separated list of database name that are not interested").Default("postgres,template0,template1").Envar("PG_EXPORTER_EXCLUDE_DATABASES").String()
	//labelDatabase    = kingpin.Flag("label-databases", "Label name for auto-filled database label (affect queries without cluster_level flag)").Default("datname").Envar("PG_EXPORTER_LABEL_DATABASES").String()
)

/**********************************************************************************************\
*                                       Constants                                              *
\**********************************************************************************************/
// PostgresVersion is postgres server num in machine readable format
type PostgresVersion int

// ColumnUsage hold how to dealing with specific column of metric result
type ColumnUsage int

// ColumnUsage: Discard, Label, Counter, Gauge
const (
	ColumnDiscard ColumnUsage = iota // Ignore this column (when SELECT *)
	ColumnLabel   ColumnUsage = iota // Use this column as a label
	ColumnCounter ColumnUsage = iota // Use this column as a counter
	ColumnGauge   ColumnUsage = iota // Use this column as a gauge
)

/**********************************************************************************************\
*                                       Exporter                                               *
\**********************************************************************************************/
// Exporter implement prometheus.Collector interface
// exporter contains one or more (auto-discover-database) servers that can scrape metrics with Query
type Exporter struct {
	// config params provided from ExporterOpt
	dsn               string            // primary dsn
	configPath        string            // config file path /directory
	disableCache      bool              // always execute query when been scrapped
	autoDiscovery     bool              // discovery other database on primary server
	excludedDatabases []string          // excluded database for auto discovery
	constLabels       prometheus.Labels // prometheus const k=v labels

	// internal states
	server  *Server            // primary server
	servers map[string]*Server // auto discovered servers (except primary & excluded database)
	queries []Query            // metrics query definition

	// exporter level metrics
	pgUp        prometheus.Gauge   // primary target server is alive ?
	serverCount prometheus.Gauge   // total active target servers
	duration    prometheus.Gauge   // last scrape duration
	error       prometheus.Gauge   // last scrape error
	errorCount  prometheus.Counter // error scrape count
	totalCount  prometheus.Counter // total scrape count
}

/**************************************************************\
* Exporter Options
\**************************************************************/

// ExporterOpt configures Exporter
type ExporterOpt func(*Exporter) error

// WithConfig add config path to Exporter
func WithConfig(configPath string) ExporterOpt {
	return func(e *Exporter) error {
		if e.configPath == "" {
			log.Warnf("run without config")
		} else {
			log.Debugf("loading config file %s", e.configPath)
		}
		e.configPath = configPath
		return nil
	}
}

// WithConstLabels add const label to exporter. 0 length label returns nil
func WithConstLabels(s string) ExporterOpt {
	return func(e *Exporter) error {
		e.constLabels = parseConstLabels(s)
		return nil
	}
}

// WithCacheDisabled set cache param to exporter
func WithCacheDisabled(disableCache bool) ExporterOpt {
	return func(e *Exporter) error {
		e.disableCache = disableCache
		return nil
	}
}

// NewExporter construct a PG Exporter instance for given dsn
func NewExporter(dsn string, opts ...ExporterOpt) (e *Exporter, err error) {
	e = &Exporter{dsn: dsn}
	for _, opt := range opts {
		if err = opt(e); err != nil {
			return nil, err
		}
	}

	if e.queries, err = LoadQueryConfig(e.configPath); err != nil {
		return nil, fmt.Errorf("fail loading config file %s: %w", e.configPath, err)
	}
	log.Debugf("exporter init with %d queries load", len(e.queries))

	// TODO: multi server discovery
	// note here the server is still not connected. it will trigger connecting when being scrapped
	if e.server, err = NewServer(
		dsn,
		WithServerConstLabel(e.constLabels),
		WithServerCacheDisabled(e.disableCache),
	); err != nil {
		return nil, fmt.Errorf("fail connecting to server %s:%w", shadowDSN(dsn), err)
	}
	if err = e.server.Plan(e.queries); err != nil {
		return nil, fmt.Errorf("fail Plan queries for server %s: %w", shadowDSN(e.server.dsn), err)
	}
	e.setupInternalMetrics()
	return
}

// Close will close all server in this exporter
func (e *Exporter) Close() {
	if e.server != nil {
		e.Close()
	}
}

// setupInternalMetrics will init internal metrics
func (e *Exporter) setupInternalMetrics() {
	e.pgUp = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   "pg",
		Name:        "up",
		Help:        "Whether the last scrape of metrics from PostgreSQL was able to connect to the server (1 for yes, 0 for no).",
		ConstLabels: e.constLabels,
	})
	e.totalCount = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   "pg",
		Subsystem:   "exporter",
		Name:        "scrapes_total",
		Help:        "Total number of times PostgresSQL was scraped for metrics.",
		ConstLabels: e.constLabels,
	})
	e.errorCount = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   "pg",
		Subsystem:   "exporter",
		Name:        "scrapes_error",
		Help:        "Total number of times PostgresSQL was scraped for metrics.",
		ConstLabels: e.constLabels,
	})
	e.duration = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   "pg",
		Subsystem:   "exporter",
		Name:        "last_scrape_duration",
		Help:        "Duration of the last scrape of metrics from PostgresSQL.",
		ConstLabels: e.constLabels,
	})
	e.error = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   "pg",
		Subsystem:   "exporter",
		Name:        "last_scrape_error",
		Help:        "Whether the last scrape of metrics from PostgreSQL resulted in an error (1 for error, 0 for success).",
		ConstLabels: e.constLabels,
	})
}

// Collect implement prometheus.Collector
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	// TODO: multi server support
	scrapeStart := time.Now()
	// only fatal error will be reported here (or skip via skip_error option)
	if err := e.server.Scrape(ch); err != nil {
		log.Errorf("fail scraping metrics from server %s: %s", shadowDSN(e.server.dsn), err.Error())
		e.pgUp.Set(0)
		e.error.Set(1)
		e.errorCount.Add(1)
	} else {
		e.pgUp.Set(1)
		e.error.Set(0)
	}
	e.totalCount.Add(1)
	e.duration.Set(time.Now().Sub(scrapeStart).Seconds())

	// collect internal metrics
	ch <- e.totalCount
	ch <- e.errorCount
	ch <- e.duration
	ch <- e.error
	ch <- e.pgUp
}

// Describe implement prometheus.Collector
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	e.server.Describe(ch)
}

// DumpQueries will print all registered queries
func (e *Exporter) Dump() {
	// TODO: Multiple Server
	if e.server != nil {
		e.server.Dump()
	}
}

/**********************************************************************************************\
*                                       Server                                                 *
\**********************************************************************************************/

// Server represent a postgres connection, with additional fact, conf, runtime info
type Server struct {
	*sql.DB               // database instance
	dsn        string     // data source name
	scrapeLock sync.Mutex // scrape lock avoid concurrent call

	// postgres fact gather from server
	version   PostgresVersion // pg version
	standby   bool            // is server a standby ?
	pgbouncer bool            // is this a pgbouncer server ?
	datname   string          // dataname of current connection

	// query
	queries      []string                  // query sequence
	instances    map[string]*QueryInstance // query instance
	labels       prometheus.Labels         // constant label that inject into metrics
	disableCache bool                      // force executing
}

// ServerOpt configures Server
type ServerOpt func(*Server)

// WithServerConstLabel copy constant label to server
func WithServerConstLabel(labels prometheus.Labels) ServerOpt {
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

// WithCacheDisabled set cache param to exporter
func WithServerCacheDisabled(disableCache bool) ServerOpt {
	return func(s *Server) {
		s.disableCache = disableCache
	}
}

// NewServer will connect to target using given dsn
func NewServer(dsn string, opts ...ServerOpt) (s *Server, err error) {
	s = &Server{
		dsn: dsn,
	}
	for _, opt := range opts {
		opt(s)
	}
	// connect, ping, and get db version
	return s, nil
}

// Connect will issue a connection to server
func (s *Server) Connect() (err error) {
	if s.DB, err = sql.Open("postgres", s.dsn); err != nil {
		return
	}
	s.DB.SetMaxIdleConns(1)
	s.DB.SetMaxOpenConns(1)

	var pgVersion PostgresVersion
	err = s.DB.QueryRow(`SHOW server_version_num;`).Scan(&pgVersion)
	if err != nil {
		return fmt.Errorf("fail fetching server version: %w", err)
	}
	s.version = pgVersion

	var isStandby bool
	var datname string
	err = s.DB.QueryRow(`SELECT current_catalog, pg_is_in_recovery();`).Scan(&datname, &isStandby)
	if err != nil {
		return fmt.Errorf("fail fetching server version: %w", err)
	}
	s.version = pgVersion
	log.Debugf("server %s connected, pg version = %v", shadowDSN(s.dsn), s.version)

	return nil
}

// Close will close internal sql.DB connection
func (s *Server) Close() {
	_ = s.DB.Close()
}

// Plan will register queries that option fit server's fact
func (s *Server) Plan(queries []Query) error {
	// connect database and fetch version
	queryInstances := make(map[string]*QueryInstance, 0)
	querySequence := make([]string, 0)
	for i, query := range queries {
		// check version compatibility
		if !query.VersionCompatible(s.version) {
			log.Infof("query %s is not planned for server %s, query version not compatible with %v", query.Name, shadowDSN(s.dsn), s.version)
			continue
		}
		// skip primary query on primary instance
		if query.SkipPrimary && !s.standby {
			log.Infof("query %s is not planned for server %s, query version not compatible with %v", query.Name, shadowDSN(s.dsn), s.version)
			continue
		}
		// skip standby query on standby instance
		if query.SkipStandby && s.standby {
			log.Infof("query %s is not planned for server %s, query version not compatible with %v", query.Name, shadowDSN(s.dsn), s.version)
			continue
		}
		queryInstances[query.Name] = queries[i].SpawnInstance(
			WithLabels(s.labels),
			WithServer(s),
		)
		querySequence = append(querySequence, query.Name)
	}
	s.instances = queryInstances
	s.queries = querySequence
	log.Debugf("server %s planned with %d of %d queries", shadowDSN(s.dsn), len(queryInstances), len(queries))
	return nil
}

// Scrape implement prometheus.Collector interface
func (s *Server) Scrape(ch chan<- prometheus.Metric) (err error) {
	scrapeStart := time.Now()
	s.scrapeLock.Lock()
	defer s.scrapeLock.Unlock()

	// lazy connection
	if s.DB == nil {
		log.Infof("establish new connection to %s", shadowDSN(s.dsn))
		if err = s.Connect(); err != nil {
			log.Infof("fail establishing new connection to %s: %s", shadowDSN(s.dsn), err.Error())
			return err
		}
	}

	// for each query: check cache and do query if necessary
	for _, queryName := range s.queries {
		if err := s.instances[queryName].Scrape(ch, s.DB, s.disableCache); err != nil {
			// continue if query have skip_error option
			if s.instances[queryName].Query.SkipError {
				log.Warnf("scrape server %s query %s failed, skip: %w", shadowDSN(s.dsn), queryName, err)
				continue
			} else {
				log.Errorf("scrape server %s query %s failed: %w", shadowDSN(s.dsn), queryName, err)
				return err
			}
		}
	}

	duration := time.Now().Sub(scrapeStart)
	log.Debugf("server %s scrape complete, duration: %v", shadowDSN(s.dsn), duration)
	return nil
}

// Describe implement prometheus.Collector
func (s *Server) Describe(ch chan<- *prometheus.Desc) {
	s.scrapeLock.Lock()
	defer s.scrapeLock.Unlock()

	for _, instance := range s.instances {
		instance.Describe(ch)
	}
}

// DumpQueries will print all registered queries
func (s *Server) Dump() {
	for _, queryName := range s.queries {
		instance := s.instances[queryName]
		lableString := strings.Join(instance.Query.LabelNames, ",")
		fmt.Printf("\n\n- %-20s\n", queryName)
		for _, m := range instance.Query.MetricNames {
			column := instance.Query.GetColumn(m)
			if column != nil {
				fmt.Printf("  - %-60s\t# %s\n", fmt.Sprintf("%s_%s{%s}", queryName, column.Name, lableString), column.Desc)
			}
		}
	}
}

/**********************************************************************************************\
*                                        Query                                                 *
\**********************************************************************************************/

// Query hold the information of how to fetch metric from database and parse them
type Query struct {
	Name    string   `yaml:"name"`    // metrics namespace
	SQL     string   `yaml:"queries"` // SQL command to fetch metrics
	Columns []Column `yaml:"columns"` // result column array

	// control query behavior
	MinVersion   PostgresVersion `yaml:"min_version"`   // minimal supported version
	MaxVersion   PostgresVersion `yaml:"max_version"`   // maximal supported version
	ClusterLevel bool            `yaml:"cluster_level"` // only execute once on same cluster
	SkipPrimary  bool            `yaml:"skip_primary"`  // skip when target is primary
	SkipStandby  bool            `yaml:"skip_standby"`  // skip when target is standby
	SkipError    bool            `yaml:"skip_error"`    // skip errors of this query
	CacheSeconds time.Duration   `yaml:"cache_seconds"` // caching expiration timeout in seconds
	Timeout      time.Duration   `yaml:"timeout"`       // query timeout in microseconds
	QueryIndex   int             `yaml:"index"`         // sequence of this query, implies in config order

	// metrics parsing auxiliaries
	LabelNames  []string       `yaml:"labels"`  // column (name) that used as label
	MetricNames []string       `yaml:"metrics"` // column (name) that used as metric
	ColumnIndex map[string]int // column name to (config) column index
}

// Column holds information about metric column
type Column struct {
	Name string `yaml:"name"`
	//Index int         `yaml:"-"`
	Usage ColumnUsage `yaml:"usage"`
	Desc  string      `yaml:"description"`
}

// GetColumn return Column struct of given column name, or nil if not found
func (q *Query) GetColumn(columnName string) *Column {
	if columnIndex, found := q.ColumnIndex[columnName]; found {
		return &q.Columns[columnIndex]
	}
	return nil
}

// VersionCompatible checks whether this query is compatible with given version
// the version should be given in PG source int style, i.e. 120100 represent 12.1
func (q *Query) VersionCompatible(version PostgresVersion) bool {
	if version == 0 {
		return true // version = 0 will skip version check
	}
	if q.MinVersion != 0 && version < q.MinVersion {
		return false
	}
	if q.MaxVersion != 0 && version > q.MaxVersion {
		return false
	}
	return true
}

// SpawnInstance will create QueryInstance from Query
func (q *Query) SpawnInstance(opts ...QueryInstanceOpt) *QueryInstance {
	return NewQueryInstance(q, opts...)
}

/**********************************************************************************************\
*                                   Query Instance                                             *
\**********************************************************************************************/

// QueryInstance holds runtime information of a Query running on a Server
// Including metric cache, descriptors, and some constant labels
type QueryInstance struct {
	*Query
	labels      prometheus.Labels           // const labels from server, set on init
	descriptors map[string]*prometheus.Desc // maps column index to descriptor, build on init

	// runtime information
	server     *Server
	lock       sync.RWMutex        // access lock
	result     []prometheus.Metric // cached metrics
	err        error
	lastScrape time.Time // real execution will fresh this
}

// QueryInstanceOpt configures QueryInstance
type QueryInstanceOpt func(*QueryInstance)

// WithLabels set the labels for query instance
func WithLabels(labels prometheus.Labels) QueryInstanceOpt {
	return func(q *QueryInstance) {
		q.labels = labels
	}
}

// WithServer inject a server into query instance
func WithServer(s *Server) QueryInstanceOpt {
	return func(q *QueryInstance) {
		q.server = s
	}
}

// NewQueryInstance will generate query instance from query, and optional const labels
func NewQueryInstance(q *Query, opts ...QueryInstanceOpt) *QueryInstance {
	qi := &QueryInstance{
		Query:  q,
		result: make([]prometheus.Metric, 0),
	}
	for _, opt := range opts {
		opt(qi)
	}

	qi.makeDescMap()
	return qi
}

// Describe implement prometheus.Collector
func (q *QueryInstance) Describe(ch chan<- *prometheus.Desc) {
	q.sendDescriptors(ch)
}

// Collect implement prometheus.Collector
func (q *QueryInstance) Collect(ch chan<- prometheus.Metric) {
	if q.err = q.Execute(); q.err != nil {
		log.Errorf("%s", q.err.Error())
		return
	}
	q.err = nil
	q.SendMetrics(ch)
}

// SendMetrics will send cached result to ch
func (q *QueryInstance) SendMetrics(ch chan<- prometheus.Metric) {
	q.lock.RLock()
	defer q.lock.RUnlock()
	for _, metric := range q.result {
		ch <- metric
	}
}

// Scrape will fetch metrics from given db
func (q *QueryInstance) Scrape(ch chan<- prometheus.Metric, db *sql.DB, disableCache bool) (err error) {
	if disableCache || q.CacheExpire() {
		if err = q.Execute(); err != nil {
			return err
		}
	}
	// read cache and send
	q.lock.RLock()
	defer q.lock.RUnlock()
	for _, metric := range q.result {
		ch <- metric
	}
	return nil
}

// makeDescMap will generate descriptor map from Query
func (q *QueryInstance) makeDescMap() {
	q.lock.Lock()
	defer q.lock.Unlock()
	descriptors := make(map[string]*prometheus.Desc, 0)
	for _, metricName := range q.MetricNames {
		metricColumn := q.GetColumn(metricName)
		if metricColumn == nil {
			log.Warnf("query %s column %s not found in config", q.Name, metricName)
			continue
		}
		descriptors[metricColumn.Name] = prometheus.NewDesc(
			fmt.Sprintf("%s_%s", q.Name, metricColumn.Name),
			metricColumn.Desc, q.LabelNames, q.labels,
		)
	}
	q.descriptors = descriptors
}

func (q *QueryInstance) sendDescriptors(ch chan<- *prometheus.Desc) {
	q.lock.RLock()
	defer q.lock.RUnlock()
	for _, desc := range q.descriptors {
		ch <- desc
	}
}

// Execute will run this query to given source, transform result into metrics and stored in cache
func (q *QueryInstance) Execute(dbs ...*sql.DB) error {
	q.lock.Lock()
	defer q.lock.Unlock()
	q.result = q.result[:0] // reset cache

	var db *sql.DB
	if len(dbs) > 0 && dbs[0] != nil {
		db = dbs[0]
	} else if q.server != nil && q.server.DB != nil {
		db = q.server.DB
	} else {
		return fmt.Errorf("query %s does not have a valid db runner", q.Name)
	}

	rows, err := db.Query(q.SQL)
	if err != nil {
		return fmt.Errorf("query %s failed: %w", q.Name, err)
	}
	defer rows.Close() // nolint: errcheck

	// get result metadata for dynamic name lookup, prepare line cache
	columnNames, err := rows.Columns()
	if err != nil {
		return fmt.Errorf("query %s fail retriving rows meta: %w", q.Name, err)
	}
	columnIndexes := make(map[string]int, len(columnNames)) // column name to index
	for i, n := range columnNames {
		columnIndexes[n] = i
	}
	nColumn := len(columnNames)
	colData := make([]interface{}, nColumn)
	colArgs := make([]interface{}, nColumn)
	for i := range colData {
		colArgs[i] = &colData[i]
	}
	// warn if column count not match
	if len(columnNames) != len(q.Columns) {
		log.Warnf("query %s column count not match, result %d ≠ config %d", q.Name, len(columnNames), len(q.Columns))
	}

	// scan loop, for each row, extract labels first, then for each metric column, generate a new metric
	for rows.Next() {
		err = rows.Scan(colArgs...)
		if err != nil {
			return fmt.Errorf("fail scanning rows: %w", err)
		}

		// get labels, sequence matters, empty string for null or bad labels
		// labelSequence -> labelName -> columnName -> columnIndex -> columnData -> labelValue
		labels := make([]string, len(q.LabelNames))
		for i, labelName := range q.LabelNames {
			if dataIndex, found := columnIndexes[labelName]; found {
				labels[i], _ = castString(colData[dataIndex])
			} else { //if label column is not found in result, we just warn and send a empty string
				log.Warnf("missing label %s.%s", q.Name, labelName)
				labels[i] = ""
			}
		}

		// get metrics, warn if column not exist
		for _, metricName := range q.MetricNames {
			if dataIndex, found := columnIndexes[metricName]; found { // the metric column is found in result
				metric, _ := castFloat64(colData[dataIndex])
				metricColumn := q.GetColumn(metricName)
				if metricColumn == nil {
					log.Warnf("missing %s.%s in config", q.Name, metricName)
					continue
				}
				usage := prometheus.GaugeValue // default
				if metricColumn.Usage == ColumnCounter {
					usage = prometheus.CounterValue
				}
				if desc, found := q.descriptors[metricColumn.Name]; found {
					q.result = append(q.result, prometheus.MustNewConstMetric(desc, usage, metric, labels...))
				} else {
					log.Warnf("metric column %s does not have corresponding descriptor", metricColumn.Name)
				}
			} else {
				log.Debugf("missing %s.%s in result", q.Name, metricName)
			}
		}
	}
	q.lastScrape = time.Now()
	return nil
}

// CacheExpire report whether this instance needs actual execution
func (q *QueryInstance) CacheExpire() bool {
	if time.Now().Sub(q.lastScrape) > q.CacheSeconds {
		return true
	}
	return false
}

/**********************************************************************************************\
*                                    Query Config                                              *
\**********************************************************************************************/
// queryConfig defines the config file structure
type queryConfig struct {
	Query   string `yaml:"query"`
	Metrics []map[string]struct {
		Usage       string `yaml:"usage"`
		Description string `yaml:"description"`
	} `yaml:"metrics"`
	Options struct {
		MinVersion   PostgresVersion `yaml:"min_version"`   // Pg Server Version Num Format: e.g 90600, 110100
		MaxVersion   PostgresVersion `yaml:"max_version"`   // Same as above
		ClusterLevel bool            `yaml:"cluster_level"` // only execute once for a cluster. false means every database will execute it
		SkipPrimary  bool            `yaml:"skip_primary"`  //
		SkipStandby  bool            `yaml:"skip_standby"`
		SkipError    bool            `yaml:"skip_error"`
		Timeout      uint64          `yaml:"timeout"`
		CacheSeconds uint64          `yaml:"cache_seconds"`
	} `yaml:"options"`
}

// ParseQueryConfig will turn config file content to array of Query structure
func ParseQueryConfig(content []byte) (res []Query, err error) {
	var queriesConfig = make([]map[string]queryConfig, 0)
	if err = yaml.Unmarshal(content, &queriesConfig); err != nil {
		return nil, fmt.Errorf("malformed config file: %w", err)
	}
	res = make([]Query, len(queriesConfig))

	// build Query struct from queryConfig
	for index, query := range queriesConfig { // array of one-entry map
		for ns, query := range query {
			res[index] = Query{
				Name:         ns,
				SQL:          query.Query,
				ClusterLevel: query.Options.ClusterLevel,
				SkipPrimary:  query.Options.SkipPrimary,
				SkipStandby:  query.Options.SkipStandby,
				SkipError:    query.Options.SkipError,
				Timeout:      time.Duration(int64(query.Options.Timeout) * int64(time.Microsecond)),
				CacheSeconds: time.Duration(int64(query.Options.CacheSeconds) * int64(time.Second)),
				MinVersion:   query.Options.MinVersion,
				MaxVersion:   query.Options.MaxVersion,
				QueryIndex:   index,
			}

			columns := make([]Column, len(query.Metrics))
			var labelNames, metricNames []string
			var columnIndex = make(map[string]int)

			// find out labels and metrics
			for colIndex, column := range query.Metrics {
				for colName, columnConfig := range column { // 1 entry map
					colUsage, err := parseColumnUsage(columnConfig.Usage)
					if err != nil {
						return nil, fmt.Errorf("fail parsing config: %w", err)
					}
					switch colUsage {
					case ColumnLabel:
						labelNames = append(labelNames, colName)
					case ColumnCounter, ColumnGauge:
						metricNames = append(metricNames, colName)
					}
					columnIndex[colName] = colIndex
					columns[colIndex] = Column{
						Name: colName,
						//Index: colIndex,
						Usage: colUsage,
						Desc:  columnConfig.Description,
					}
				}
			}
			res[index].Columns = columns
			res[index].LabelNames = labelNames
			res[index].MetricNames = metricNames
			res[index].ColumnIndex = columnIndex
		}
	}
	return res, nil
}

// LoadQueryConfig will read config file (or dir) and parse into []Query
func LoadQueryConfig(filename string) (res []Query, err error) {
	content, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("fail reading config file: %w", err)
	}
	return ParseQueryConfig(content)
}

/**********************************************************************************************\
*                                     Auxiliaries                                              *
\**********************************************************************************************/
// parseColumnUsage will turn column usage string to enum
func parseColumnUsage(s string) (ColumnUsage, error) {
	var u ColumnUsage
	var err error
	switch strings.ToUpper(s) {
	case "DISCARD":
		u = ColumnDiscard
	case "LABEL":
		u = ColumnLabel
	case "COUNTER":
		u = ColumnCounter
	case "GAUGE":
		u = ColumnGauge
	default:
		err = fmt.Errorf("malformed column usage given: %s", s)
	}
	return u, err
}

// castString will force interface{} into float64
func castFloat64(t interface{}) (float64, bool) {
	switch v := t.(type) {
	case int64:
		return float64(v), true
	case float64:
		return v, true
	case time.Time:
		return float64(v.Unix()), true
	case []byte:
		strV := string(v)
		result, err := strconv.ParseFloat(strV, 64)
		if err != nil {
			log.Warnf("fail casting []byte to float64: %v", t)
			return math.NaN(), false
		}
		return result, true
	case string:
		result, err := strconv.ParseFloat(v, 64)
		if err != nil {
			log.Warnf("fail casting string to float64: %v", t)
			return math.NaN(), false
		}
		return result, true
	case bool:
		if v {
			return 1.0, true
		}
		return 0.0, true
	case nil:
		return math.NaN(), true
	default:
		return math.NaN(), false
	}
}

// castString will force interface{} into string
func castString(t interface{}) (string, bool) {
	switch v := t.(type) {
	case int64:
		return fmt.Sprintf("%v", v), true
	case float64:
		return fmt.Sprintf("%v", v), true
	case time.Time:
		return fmt.Sprintf("%v", v.Unix()), true
	case nil:
		return "", true
	case []byte:
		// Try and convert to string
		return string(v), true
	case string:
		return v, true
	case bool:
		if v {
			return "true", true
		}
		return "false", true
	default:
		return "", false
	}
}

// parseConstLabels turn param string into prometheus.Labels
func parseConstLabels(s string) prometheus.Labels {
	labels := make(prometheus.Labels, 0)
	s = strings.TrimSpace(s)
	if len(s) == 0 {
		return nil
	}

	parts := strings.Split(s, ",")
	for _, p := range parts {
		keyValue := strings.Split(strings.TrimSpace(p), "=")
		if len(keyValue) != 2 {
			log.Errorf(`malformed labels format %q, should be "key=value"`, p)
			continue
		}
		key := strings.TrimSpace(keyValue[0])
		value := strings.TrimSpace(keyValue[1])
		if key == "" || value == "" {
			continue
		}
		labels[key] = value
	}
	if len(labels) == 0 {
		return nil
	}

	return labels
}

// shadowDSN will hide password part of dsn
func shadowDSN(dsn string) string {
	pDSN, err := url.Parse(dsn)
	if err != nil {
		return ""
	}
	// Blank user info if not nil
	if pDSN.User != nil {
		pDSN.User = url.UserPassword(pDSN.User.Username(), "PASSWORD")
	}
	return pDSN.String()
}

/**********************************************************************************************\
*                                          Main                                                *
\**********************************************************************************************/

func main() {
	kingpin.Version(fmt.Sprintf("postgres_exporter %s (built with %s)\n", Version, runtime.Version()))
	log.AddFlags(kingpin.CommandLine)
	kingpin.Parse()

	var exporter *Exporter
	var err error
	if exporter, err = NewExporter(
		*dataSourceName,
		WithConfig(*configPath),
		WithConstLabels(*constLabels),
		WithCacheDisabled(*disableCache),
	); err != nil {
		log.Fatalf("fail creating pg_exporter: %w", err)
		os.Exit(-1)
	}

	if *dumpMetrics {
		exporter.Dump()
		os.Exit(0)
	}

	if err = log.Base().SetLevel(*logLevel); err != nil {
		log.Fatalf("fail setting log level to %s: %w", *logLevel, err)
	}
	prometheus.MustRegister(exporter)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		_, _ = w.Write([]byte(`<html><head><title>PG Exporter</title></head><body><h1>PG Exporter</h1><p><a href='` + *metricPath + `'>Metrics</a></p></body></html>`))
	})
	http.Handle(*metricPath, promhttp.Handler())
	log.Infof("pg_exporter for %s start, listen on http://%s%s", shadowDSN(*dataSourceName), *listenAddress, *metricPath)
	defer exporter.Close()
	log.Fatal(http.ListenAndServe(*listenAddress, nil))
}
