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
	logLevel      = kingpin.Flag("log-level", "log-level").Default("Debug").Envar("PG_EXPORTER_LOG_LEVEL").String()

	// exporter settings
	dataSourceName = kingpin.Flag("dsn", "postgres connect string").Default("postgres://:5432/postgres?host=/tmp&sslmode=disable").Envar("PG_EXPORTER_DSN").String()
	configPath     = kingpin.Flag("config", "Path to config files").Default("pg_exporter.yaml").Envar("PG_EXPORTER_CONFIG_PATH").String()
	constLabels    = kingpin.Flag("label", "Comma separated list label=value pair").Default("").Envar("PG_EXPORTER_CONST_LABELS").String()
	disableCache   = kingpin.Flag("disable-cache", "force not using cache").Default("false").Envar("PG_EXPORTER_DISABLE_CACHE").Bool()
	dumpMetrics    = kingpin.Flag("dump", "dry run and dump metric maps only").Default("false").Bool()

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
type Exporter struct {
	Server       *Server
	Query        []Query
	dsn          string
	configPath   string
	excludedDB   []string
	labels       prometheus.Labels
	disableCache bool

	// internal metrics
	pgUp       prometheus.Gauge
	totalCount prometheus.Counter
	errorCount prometheus.Counter
	duration   prometheus.Gauge
	error      prometheus.Gauge
}

// ExporterOpt configures Exporter
type ExporterOpt func(*Exporter)

// WithConfig add config path to Exporter
func WithConfig(filename string) ExporterOpt {
	return func(e *Exporter) {
		e.configPath = filename
	}
}

// WithConstLabels add const label to exporter. 0 length label returns nil
func WithConstLabels(s string) ExporterOpt {
	return func(e *Exporter) {
		e.labels = parseConstLabels(s)
	}
}

// WithCacheDisabled set cache param to exporter
func WithCacheDisabled(disableCache bool) ExporterOpt {
	return func(e *Exporter) {
		e.disableCache = disableCache
	}
}

// NewExporter returns a new pg exporter instance for given dsn
func NewExporter(dsn string, opts ...ExporterOpt) (e *Exporter, err error) {
	e = &Exporter{dsn: dsn}
	for _, opt := range opts {
		opt(e)
	}

	if e.configPath == "" {
		log.Warnf("config file path not set, something wrong?")
	} else {
		log.Debugf("loading config file %s", e.configPath)
	}
	if e.Query, err = LoadQueryConfig(e.configPath); err != nil {
		return nil, fmt.Errorf("fail loading config file %s: %w", e.configPath, err)
	}
	log.Debugf("exporter init with %d queries load", len(e.Query))

	// TODO: multi server discovery
	// note here the server is still not connected. it will trigger connecting when being scrapped
	if e.Server, err = NewServer(
		dsn,
		WithServerConstLabel(e.labels),
		WithServerCacheDisabled(e.disableCache),
	); err != nil {
		return nil, fmt.Errorf("fail connecting to server %s:%w", shadowDSN(dsn), err)
	}
	if err = e.Server.Planning(e.Query); err != nil {
		return nil, fmt.Errorf("fail planning queries for server %s: %w", shadowDSN(e.Server.dsn), err)
	}
	e.setupInternalMetrics()
	return
}

// Close will close all server in this exporter
func (e *Exporter) Close() {
	if e.Server != nil {
		e.Close()
	}
}

// setupInternalMetrics will init internal metrics
func (e *Exporter) setupInternalMetrics() {
	e.pgUp = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   "pg",
		Name:        "up",
		Help:        "Whether the last scrape of metrics from PostgreSQL was able to connect to the server (1 for yes, 0 for no).",
		ConstLabels: e.labels,
	})
	e.totalCount = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   "pg",
		Subsystem:   "exporter",
		Name:        "scrapes_total",
		Help:        "Total number of times PostgresSQL was scraped for metrics.",
		ConstLabels: e.labels,
	})
	e.errorCount = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   "pg",
		Subsystem:   "exporter",
		Name:        "scrapes_error",
		Help:        "Total number of times PostgresSQL was scraped for metrics.",
		ConstLabels: e.labels,
	})
	e.duration = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   "pg",
		Subsystem:   "exporter",
		Name:        "last_scrape_duration",
		Help:        "Duration of the last scrape of metrics from PostgresSQL.",
		ConstLabels: e.labels,
	})
	e.error = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   "pg",
		Subsystem:   "exporter",
		Name:        "last_scrape_error",
		Help:        "Whether the last scrape of metrics from PostgreSQL resulted in an error (1 for error, 0 for success).",
		ConstLabels: e.labels,
	})
}

// Collect implement prometheus.Collector
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	// TODO: multi server support
	scrapeStart := time.Now()
	// only fatal error will be reported here (or skip via skip_error option)
	if err := e.Server.Scrape(ch); err != nil {
		log.Errorf("fail scraping metrics from server %s: %s", shadowDSN(e.Server.dsn), err.Error())
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
	e.Server.Describe(ch)
}

// DumpQueries will print all registered queries
func (e *Exporter) Dump() {
	// TODO: Multiple Server
	if e.Server != nil {
		e.Server.Dump()
	}
}

/**********************************************************************************************\
*                                       Server                                                 *
\**********************************************************************************************/

// Server represent a connectable pg-protocol-v3 backend
type Server struct {
	dsn        string     // data source name
	db         *sql.DB    // database instance
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
	if s.db, err = sql.Open("postgres", s.dsn); err != nil {
		return
	}
	s.db.SetMaxIdleConns(1)
	s.db.SetMaxOpenConns(1)

	var pgVersion PostgresVersion
	err = s.db.QueryRow(`SHOW server_version_num;`).Scan(&pgVersion)
	if err != nil {
		return fmt.Errorf("fail fetching server version: %w", err)
	}
	s.version = pgVersion

	var isStandby bool
	var datname string
	err = s.db.QueryRow(`SELECT current_catalog, pg_is_in_recovery();`).Scan(&datname, &isStandby)
	if err != nil {
		return fmt.Errorf("fail fetching server version: %w", err)
	}
	s.version = pgVersion
	log.Debugf("server %s connected, pg version = %v", shadowDSN(s.dsn), s.version)

	return nil
}

// Close will close internal sql.DB connection
func (s *Server) Close() {
	_ = s.db.Close()
}

// Planning will register queries that option fit server's fact
func (s *Server) Planning(queries []Query) error {
	// connect database and fetch version
	queryInstances := make(map[string]*QueryInstance, 0)
	querySequence := make([]string, 0)
	for i, query := range queries {
		// check version compatibility
		if !query.VersionCompatible(s.version) {
			log.Infof("query %s is not planned for server %s, query version not compatible with %v", query.Namespace, shadowDSN(s.dsn), s.version)
			continue
		}
		// skip primary query on primary instance
		if query.SkipPrimary && !s.standby {
			log.Infof("query %s is not planned for server %s, query version not compatible with %v", query.Namespace, shadowDSN(s.dsn), s.version)
			continue
		}
		// skip standby query on standby instance
		if query.SkipStandby && s.standby {
			log.Infof("query %s is not planned for server %s, query version not compatible with %v", query.Namespace, shadowDSN(s.dsn), s.version)
			continue
		}
		queryInstances[query.Namespace] = queries[i].SpawnInstance(WithQueryInstanceConstLabels(s.labels))
		querySequence = append(querySequence, query.Namespace)
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
	if s.db == nil {
		log.Infof("establish new connection to %s", shadowDSN(s.dsn))
		if err = s.Connect(); err != nil {
			log.Infof("fail establishing new connection to %s: %s", shadowDSN(s.dsn), err.Error())
			return err
		}
	}

	// for each query: check cache and do query if necessary
	for _, queryName := range s.queries {
		if err := s.instances[queryName].Scrape(ch, s.db, s.disableCache); err != nil {
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
		for _, desc := range instance.Descriptors {
			ch <- desc
		}
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
				fmt.Printf("  - %-60s\t# %s\n", fmt.Sprintf("%s_%s{%s}", queryName, column.Name, lableString), column.Description)
			}
		}
	}
}

/**********************************************************************************************\
*                                        Query                                                 *
\**********************************************************************************************/

// Query hold the information of how to fetch metric from database and parse them
type Query struct {
	Namespace string   `yaml:"namespace"` // metrics namespace
	SQL       string   `yaml:"queries"`   // SQL command to fetch metrics
	Columns   []Column `yaml:"columns"`   // result column array

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
	Name        string      `yaml:"name"`
	Index       int         `yaml:"-"`
	Usage       ColumnUsage `yaml:"usage"`
	Description string      `yaml:"description"`
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
	Query       *Query
	Descriptors map[string]*prometheus.Desc // maps column index to descriptor
	Cache       []prometheus.Metric         // cached metrics
	CacheLock   sync.RWMutex
	Labels      prometheus.Labels
	LastScrape  time.Time
}

// QueryInstanceOpt configures QueryInstance
type QueryInstanceOpt func(*QueryInstance)

// WithQueryInstanceConstLabels is different from server const label, it is shared not copied
func WithQueryInstanceConstLabels(labels prometheus.Labels) QueryInstanceOpt {
	return func(q *QueryInstance) {
		q.Labels = labels
	}
}

// NewQueryInstance will generate query instance from query, and optional const labels
func NewQueryInstance(q *Query, opts ...QueryInstanceOpt) *QueryInstance {
	qi := &QueryInstance{
		Query: q,
		Cache: make([]prometheus.Metric, 0),
	}

	// configure query instance
	for _, opt := range opts {
		opt(qi)
	}

	// generate descriptors
	descriptors := make(map[string]*prometheus.Desc, 0)
	for _, metricName := range q.MetricNames {
		metricColumn := q.GetColumn(metricName)
		if metricColumn == nil {
			log.Warnf("query %s column %s not found in config", q.Namespace, metricName)
			continue
		}
		descriptors[metricColumn.Name] = prometheus.NewDesc(
			fmt.Sprintf("%s_%s", q.Namespace, metricColumn.Name),
			metricColumn.Description, q.LabelNames, qi.Labels,
		)
	}
	qi.Descriptors = descriptors
	return qi
}

// Scrape will fetch metrics from given db
func (q *QueryInstance) Scrape(ch chan<- prometheus.Metric, db *sql.DB, disableCache bool) (err error) {
	if disableCache || q.CacheExpire() {
		if err = q.Execute(db); err != nil {
			return err
		}
	}
	// read cache and send
	q.CacheLock.RLock()
	defer q.CacheLock.RUnlock()
	for _, metric := range q.Cache {
		ch <- metric
	}
	return nil
}

// Execute will run this query to given source and transform result into metrics
func (q *QueryInstance) Execute(db *sql.DB) error {
	q.CacheLock.Lock()
	defer q.CacheLock.Unlock()
	q.Cache = q.Cache[:0] // reset cache

	rows, err := db.Query(q.Query.SQL)
	if err != nil {
		return fmt.Errorf("query %s failed: %w", q.Query.Namespace, err)
	}
	defer rows.Close() // nolint: errcheck

	// get result metadata
	columnNames, err := rows.Columns()
	if err != nil {
		return fmt.Errorf("query %s fail retriving rows meta: %w", q.Query.Namespace, err)
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
	if len(columnNames) != len(q.Query.Columns) {
		log.Warnf("query %s result column %d not match config %d", q.Query.Namespace, len(columnNames), len(q.Query.Columns))
	}

	//var metrics = make([]prometheus.Metric, 0)
	for rows.Next() {
		err = rows.Scan(colArgs...)
		if err != nil {
			return fmt.Errorf("fail scanning rows: %w", err)
		}

		// get labels, sequence matters, empty string for null or bad labels
		// labelSequence -> labelName -> columnName -> columnIndex -> columnData -> labelValue
		labels := make([]string, len(q.Query.LabelNames))
		for i, labelName := range q.Query.LabelNames {
			if dataIndex, found := columnIndexes[labelName]; found { // the label column is found in result
				labels[i], _ = castString(colData[dataIndex])
			} else {
				log.Warnf("missing label %s.%s", q.Query.Namespace, labelName)
				labels[i] = ""
			}
		}

		// get metrics, append to list, omit if column not exist
		for _, metricName := range q.Query.MetricNames {
			if dataIndex, found := columnIndexes[metricName]; found { // the metric column is found in result
				metric, _ := castFloat64(colData[dataIndex])
				metricColumn := q.Query.GetColumn(metricName)
				if metricColumn == nil {
					log.Warnf("missing %s.%s in config", q.Query.Namespace, metricName)
					continue
				}
				usage := prometheus.GaugeValue // default
				if metricColumn.Usage == ColumnCounter {
					usage = prometheus.CounterValue
				}
				if desc, found := q.Descriptors[metricColumn.Name]; found {
					q.Cache = append(q.Cache, prometheus.MustNewConstMetric(desc, usage, metric, labels...))
				} else {
					log.Warnf("metric column %s does not have corresponding descriptor", metricColumn.Name)
				}
			} else {
				log.Debugf("missing %s.%s in result", q.Query.Namespace, metricName)
			}
		}
	}
	q.LastScrape = time.Now()
	return nil
}

// CacheExpire report whether this instance needs actual execution
func (q *QueryInstance) CacheExpire() bool {
	if time.Now().Sub(q.LastScrape) > q.Query.CacheSeconds {
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
				Namespace:    ns,
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
						Name:        colName,
						Index:       colIndex,
						Usage:       colUsage,
						Description: columnConfig.Description,
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
	if exporter, err = NewExporter(*dataSourceName,
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
