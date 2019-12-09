package main

import (
	"bytes"
	"database/sql"
	"fmt"
	"io/ioutil"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

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

// Version 0.0.2
var Version = "0.0.2"

var defaultPGURL = "postgresql:///?sslmode=disable"

var (
	// exporter settings
	pgURL        = kingpin.Flag("url", "postgres connect url").String()
	configPath   = kingpin.Flag("config", "Path to config files").Default("./pg_exporter.yaml").Envar("PG_EXPORTER_CONFIG").String()
	constLabels  = kingpin.Flag("label", "Comma separated list label=value pair").Default("").Envar("PG_EXPORTER_LABELS").String()
	disableCache = kingpin.Flag("disable-cache", "force not using cache").Default("false").Envar("PG_EXPORTER_CACHE").Bool()

	// prometheus http
	listenAddress = kingpin.Flag("web.listen-address", "prometheus web server listen address").Default(":8848").Envar("PG_EXPORTER_LISTEN_ADDRESS").String()
	metricPath    = kingpin.Flag("web.telemetry-path", "Path under which to expose metrics.").Default("/metrics").Envar("PG_EXPORTER_TELEMETRY_PATH").String()
	logLevel      = kingpin.Flag("log-level", "log-level").Default("Info").Envar("PG_EXPORTER_LOG_LEVEL").String()

	// action
	explainOnly = kingpin.Flag("explain", "dry run and explain queries").Default("false").Bool()
)

/**********************************************************************************************\
*                                       Column                                                 *
\**********************************************************************************************/
const (
	DISCARD = "DISCARD" // Ignore this column (when SELECT *)
	LABEL   = "LABEL"   // Use this column as a label
	COUNTER = "COUNTER" // Use this column as a counter
	GAUGE   = "GAUGE"   // Use this column as a gauge
)

// ColumnUsage determins how to explain query result column
var ColumnUsage = map[string]bool{
	DISCARD: false,
	LABEL:   false,
	COUNTER: true,
	GAUGE:   true,
}

// Column holds metadata of query result
type Column struct {
	Name   string `yaml:"name"`
	Desc   string `yaml:"description"`
	Usage  string `yaml:"usage"`
	Rename string `yaml:"rename"`
}

// PrometheusValueType returns column's usage for prometheus
func (c *Column) PrometheusValueType() prometheus.ValueType {
	switch strings.ToUpper(c.Usage) {
	case GAUGE:
		return prometheus.GaugeValue
	case COUNTER:
		return prometheus.CounterValue
	default:
		// it's user's responsibility to make sure this is a value column
		panic(fmt.Errorf("column %s does not have a valid value type %s", c.Name, c.Usage))
	}
}

// String turns column into a line text representation
func (c *Column) String() string {
	return fmt.Sprintf("%-8s %-20s %s", c.Usage, c.Name, c.Desc)
}

/**********************************************************************************************\
*                                       Query                                                  *
\**********************************************************************************************/

// Query hold the information of how to fetch metric and parse them
type Query struct {
	Name string `yaml:"name"`  // query name, used as metric prefix
	SQL  string `yaml:"query"` // SQL command to fetch metrics

	// control query behaviour
	TTL        float64              `yaml:"ttl"`         // caching ttl, e.g. 10s, 2min
	Tags       []string             `yaml:"tags"`        // tags are user provided execution hint
	Priority   int                  `yaml:"priority"`    // execution priority, from 1 to 100 (low to high)
	MinVersion int                  `yaml:"min_version"` // minimal supported version, include
	MaxVersion int                  `yaml:"max_version"` // maximal supported version, not include
	Timeout    float64              `yaml:"timeout"`     // query execution timeout, e.g. 1.0ms, 300us
	SkipErrors bool                 `yaml:"skip_errors"` // can error be omitted?
	Metrics    []map[string]*Column `yaml:"metrics"`

	// metrics parsing auxiliaries
	Path        string             // where am I from ?
	Columns     map[string]*Column // column map
	ColumnNames []string           // column names in origin orders
	LabelNames  []string           // column (name) that used as label, sequences matters
	MetricNames []string           // column (name) that used as metric
}

var queryTemplate, _ = template.New("Query").Parse(`
┏━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
┃ {{ .Name }}
┣┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈
{{range .Columns }}┃{{.String}}
{{end}}┣┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈
┃ Tags     ┆ {{ .Tags }}
┃ TTL      ┆ {{ .TTL }}
┃ Priority ┆ 
┃ Timeout  ┆ {{ .Timeout }}
┃ SkipErr  ┆ {{ .SkipErrors }}
┃ Version  ┆ {{ .MinVersion }} - {{ .MaxVersion }}
┃ Source   ┆ {{.Path }}
┗┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈
 {{ .SQL }}
┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━`)

// Explain will turn query into text representation
func (q *Query) Explain() string {
	buf := new(bytes.Buffer)
	err := queryTemplate.Execute(buf, q)
	if err != nil {
		panic(err)
	}
	return buf.String()
}

// HasTag tells whether this query have specific tag
func (q *Query) HasTag(tag string) bool {
	for _, t := range q.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// ParseConfig turn config content into Query struct
func ParseConfig(content []byte) (queries map[string]*Query, err error) {
	queries = make(map[string]*Query, 0)
	if err = yaml.Unmarshal(content, &queries); err != nil {
		return nil, fmt.Errorf("malformed config: %w", err)
	}

	// parse additional fields
	for name, query := range queries {
		if query.Name == "" {
			query.Name = name
		}
		// parse query column info
		columns := make(map[string]*Column, len(query.Metrics))
		var allColumns, labelColumns, metricColumns []string
		for _, colMap := range query.Metrics {
			for colName, column := range colMap { // one-entry map
				if column.Name == "" {
					column.Name = colName
				}
				if _, isValid := ColumnUsage[column.Usage]; !isValid {
					return nil, fmt.Errorf("column %s have unsupported usage: %s", colName, column.Desc)
				}
				column.Usage = strings.ToUpper(column.Usage)
				switch column.Usage {
				case LABEL:
					labelColumns = append(labelColumns, column.Name)
				case GAUGE, COUNTER:
					metricColumns = append(metricColumns, column.Name)
				}
				allColumns = append(metricColumns, column.Name)
				columns[column.Name] = column
			}
		}
		query.Columns, query.ColumnNames, query.LabelNames, query.MetricNames = columns, allColumns, labelColumns, metricColumns
	}
	return
}

// ParseQuery generate a single query from config string
func ParseQuery(config string) (*Query, error) {
	queries, err := ParseConfig([]byte(config))
	if err != nil {
		return nil, err
	}
	if len(queries) == 0 {
		return nil, fmt.Errorf("no query definition found")
	}
	if len(queries) > 1 {
		return nil, fmt.Errorf("multiple query definition found")
	}
	for _, q := range queries {
		return q, nil // return the only query instance
	}
	return nil, fmt.Errorf("no query definition found")
}

// LoadConfig will read single conf file or read multiple conf file if a dir is given
// conf file in a dir will be load in alphabetic order, query with same name will overwrite predecessor
func LoadConfig(configPath string) (queries map[string]*Query, err error) {
	stat, err := os.Stat(configPath)
	if err != nil {
		return nil, fmt.Errorf("invalid config path %s: %w", configPath, err)
	}
	if stat.IsDir() { // recursively iterate conf files if a dir is given
		files, err := ioutil.ReadDir(configPath)
		if err != nil {
			return nil, fmt.Errorf("fail reading config dir %s: %w", configPath, err)
		}

		log.Debugf("load config from dir %s", configPath)
		confFiles := make([]string, 0)
		for _, conf := range files {
			if !strings.HasSuffix(conf.Name(), ".yaml") && !conf.IsDir() { // depth = 1
				continue // skip non yaml files
			}
			confFiles = append(confFiles, path.Join(configPath, conf.Name()))
		}

		queries = make(map[string]*Query, 0)
		for index, confPath := range confFiles {
			if singleQueries, err := LoadConfig(confPath); err != nil {
				log.Warnf("skip config %s due to error: %s", confPath, err.Error())
			} else {
				for name, query := range singleQueries {
					if query.Priority == 0 {
						query.Priority = 100 - index // assume you don't really provide 100 conf files
					}
					queries[name] = query // so the later one will overwrite former one
				}
			}
		}

		return queries, nil
	}

	// single file case: recursive exit condition
	if content, err := ioutil.ReadFile(configPath); err != nil {
		return nil, fmt.Errorf("fail reading config file %s: %w", configPath, err)
	} else {
		if singleQueries, err := ParseConfig(content); err != nil {
			return nil, err
		} else {
			for _, q := range singleQueries {
				q.Path = stat.Name()
			}
			log.Debugf("load config from file %s", configPath)
			return singleQueries, nil
		}
	}
}

/**********************************************************************************************\
*                                   Query Instance                                             *
\**********************************************************************************************/

// QueryInstance holds runtime information of a Query running on a Server
// It is deeply coupled with Server. Besides, it can be a collector itself
type QueryInstance struct {
	*Query
	Server *Server // It's a query, but holds a server

	// runtime information
	lock        sync.RWMutex                // access lock
	result      []prometheus.Metric         // cached metrics
	descriptors map[string]*prometheus.Desc // maps column index to descriptor, build on init
	Err         error

	lastScrape time.Time // real execution will fresh this
}

// ResultSize report last scrapped metric count
func (q *QueryInstance) ResultSize() int {
	q.lock.RLock()
	defer q.lock.RUnlock()
	return len(q.result)
}

// NewQueryInstance will generate query instance from query, and optional const labels
func NewQueryInstance(q *Query, s *Server) *QueryInstance {
	instance := &QueryInstance{
		Query:  q,
		Server: s,
		result: make([]prometheus.Metric, 0),
	}
	instance.makeDescMap()
	return instance
}

// Describe implement prometheus.Collector
func (q *QueryInstance) Describe(ch chan<- *prometheus.Desc) {
	q.sendDescriptors(ch)
}

// Collect implement prometheus.Collector
func (q *QueryInstance) Collect(ch chan<- prometheus.Metric) {
	if q.CacheExpired() || q.Server.disableCache {
		if q.Err = q.Execute(); q.Err == nil {
			q.lastScrape = q.Server.lastScrape
		}
	}
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

// makeDescMap will generate descriptor map from Query
func (q *QueryInstance) makeDescMap() {
	q.lock.Lock()
	defer q.lock.Unlock()
	descriptors := make(map[string]*prometheus.Desc, 0)

	// rename label name if label column have rename option
	labelNames := make([]string, len(q.LabelNames))
	for i, labelName := range q.LabelNames {
		labelColumn := q.Columns[labelName]
		if labelColumn.Rename != "" {
			labelNames[i] = labelColumn.Rename
		} else {
			labelNames[i] = labelColumn.Name
		}
	}

	// rename metric if metric column have a rename option
	for _, metricName := range q.MetricNames {
		metricColumn := q.Columns[metricName] // always found
		metricName := fmt.Sprintf("%s_%s", q.Name, metricColumn.Name)
		if metricColumn.Rename != "" {
			metricName = fmt.Sprintf("%s_%s", q.Name, metricColumn.Rename)
		}
		descriptors[metricColumn.Name] = prometheus.NewDesc(
			metricName, metricColumn.Desc, labelNames, q.Server.labels,
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
func (q *QueryInstance) Execute() error {
	q.lock.Lock()
	defer q.lock.Unlock()
	q.result = q.result[:0] // reset cache

	// Server must be inited before using
	rows, err := q.Server.Query(q.SQL)
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
		labels := make([]string, len(q.LabelNames))
		for i, labelName := range q.LabelNames {
			if dataIndex, found := columnIndexes[labelName]; found {
				labels[i] = castString(colData[dataIndex])
			} else {
				//if label column is not found in result, we just warn and send a empty string
				log.Warnf("missing label %s.%s", q.Name, labelName)
				labels[i] = ""
			}
		}

		// get metrics, warn if column not exist
		for _, metricName := range q.MetricNames {
			if dataIndex, found := columnIndexes[metricName]; found { // the metric column is found in result
				q.result = append(q.result, prometheus.MustNewConstMetric(
					q.descriptors[metricName], // always find desc & column via name
					q.Columns[metricName].PrometheusValueType(),
					castFloat64(colData[dataIndex]),
					labels...,
				))
			} else {
				log.Debugf("missing %s.%s in result", q.Name, metricName)
			}
		}
	}
	return nil
}

// CacheExpired report whether this instance needs actual execution
// Note you have to using Server.scrapeBegin as "now", and set that timestamp as
func (q *QueryInstance) CacheExpired() bool {
	if q.Server.lastScrape.Sub(q.lastScrape) > time.Duration(q.TTL*float64(time.Second)) {
		return true
	}
	return false
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
	Database string // database name of current server connection
	Version  int    // pg server version num
	Recovery bool   // is server recovering? (slave|standby)

	// query
	queries        []string          // query sequence
	instances      []*QueryInstance  // query instance
	labels         prometheus.Labels // constant label that inject into metrics
	disableCache   bool              // force executing, ignoring caching policy
	disableCluster bool              // cluster server will not run on this server
	pgbouncerMode  bool
	lastScrape     time.Time // cache time alignment

	// exporter level metrics
	pgUp         prometheus.Gauge     // primary target server is alive ?
	lastScrapeTs prometheus.Gauge     // total active target servers
	scrapeCount  prometheus.Counter   // total scrape count of this server (only primary is counted)
	errorCount   prometheus.Counter   // error scrape count (only primary is counted)
	error        *prometheus.GaugeVec // last scrape error
	duration     *prometheus.GaugeVec // last scrape duration

	metricCount   *prometheus.CounterVec
	queryError    *prometheus.GaugeVec
	queryDuration *prometheus.GaugeVec
}

// Name returns server's name, database name , or a shadowed DSN
func (s *Server) Name() string {
	if s.Database != "" {
		return s.Database
	}
	return shadowDSN(s.dsn)
}

// Connect will issue a connection to server
func (s *Server) Connect() (err error) {
	// needs reconnect
	if s.DB == nil || (!s.pgbouncerMode && s.DB != nil && s.DB.Ping() != nil) {
		if s.DB, err = sql.Open("postgres", s.dsn); err != nil {
			log.Errorf("fail connecting to server %s: %s", s.Name(), err.Error())
			return err
		}
		s.DB.SetMaxIdleConns(1)
		s.DB.SetMaxOpenConns(1)
	}

	// pgbouncer will skip gathering fact like version & datname ...
	if s.pgbouncerMode {
		log.Debugf("server %s connected, skip fact gathering", s.Name())
		return nil
	}

	// Gather fact
	var version int
	err = s.DB.QueryRow(`SHOW server_version_num;`).Scan(&version)
	if err != nil {
		return fmt.Errorf("fail fetching server version: %w", err)
	}
	if s.Version != version {
		log.Infof("server [%s] version changed: from [%d] to [%d]", s.Name(), s.Version, version)
	}
	s.Version = version

	var recovery bool
	var datname string
	err = s.DB.QueryRow(`SELECT current_catalog, pg_is_in_recovery();`).Scan(&datname, &recovery)
	if err != nil {
		return fmt.Errorf("fail fetching server version: %w", err)
	}
	if s.Recovery != recovery {
		log.Infof("server [%s] recovery status changed: from [%v] to [%v]", s.Name(), s.Recovery, recovery)
	}
	s.Recovery = recovery
	if s.Database != datname {
		log.Infof("server [%s] datname changed: from [%s] to [%s]", s.Name(), s.Database, datname)
	}
	s.Database = datname

	log.Debugf("server %s connected, version = %v, in recovery: %v", s.Name(), s.Version, recovery)
	return nil
}

// Plan will register queries that option fit server's fact
func (s *Server) Plan(queries map[string]*Query) error {
	// only register those matches server's fact
	instances := make([]*QueryInstance, 0)
	var installedNames, discardedNames []string
	for _, query := range queries {
		pgbouncerQuery := query.HasTag("pgbouncer")
		if (pgbouncerQuery && s.pgbouncerMode) || (!pgbouncerQuery && !s.pgbouncerMode) {
			instances = append(instances, NewQueryInstance(query, s))
			installedNames = append(installedNames, query.Name)
		} else {
			discardedNames = append(discardedNames, query.Name)
		}
	}
	// sort in priority order
	sort.Slice(instances, func(i, j int) bool {
		return instances[i].Priority > instances[j].Priority
	})

	s.instances = instances

	log.Infof("server %s planned with %d queries, %d installed", s.Name(), len(queries), len(instances))
	log.Infof("installed queries: %s", strings.Join(installedNames, ", "))
	log.Infof("discarded queries: %s", strings.Join(discardedNames, ", "))
	return nil
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
	s.scrapeLock.Lock()
	defer s.scrapeLock.Unlock()

	for _, instance := range s.instances {
		instance.Describe(ch)
	}

	// server internal metrics
	if !s.disableCluster {
		s.pgUp.Describe(ch)
		s.scrapeCount.Describe(ch)
		s.errorCount.Describe(ch)
		s.lastScrapeTs.Describe(ch)
		s.duration.Describe(ch)
		s.error.Describe(ch)
		s.queryError.Describe(ch)
		s.queryDuration.Describe(ch)
	}
}

// Collect implement prometheus.Collector interface
func (s *Server) Collect(ch chan<- prometheus.Metric) {
	s.scrapeLock.Lock()
	defer s.scrapeLock.Unlock()

	// there's errors are treated as non-fatal errors
	s.lastScrape = time.Now() // This ts is used for cache expiration check
	s.lastScrapeTs.Set(float64(s.lastScrape.Unix()))
	s.duration.Reset()
	s.queryDuration.Reset()
	s.queryError.Reset()
	s.error.Reset()

	var fail bool
	if err := s.Connect(); err != nil {
		log.Errorf("fail establishing new connection to %s: %s", s.Name(), err.Error())
		fail = true
		goto final
	}

	for _, instance := range s.instances {
		if !compatible(instance, s) {
			continue
		}
		instanceStart := time.Now()
		instance.Collect(ch)
		s.queryDuration.WithLabelValues(s.Database, instance.Name).Set(time.Now().Sub(instanceStart).Seconds())
		s.metricCount.WithLabelValues(s.Database, instance.Name).Add(float64(instance.ResultSize()))
		if instance.Err != nil {
			s.queryError.WithLabelValues(s.Database, instance.Name).Set(1)
			if !instance.SkipErrors {
				log.Errorf("server %s fail collecting metrics from instance %s, %s", s.Name(), instance.Name, instance.Err.Error())
				fail = true
				goto final
			}
		} else {
			s.queryError.WithLabelValues(s.Database, instance.Name).Set(0)
		}
	}

final:
	duration := time.Now().Sub(s.lastScrape)
	s.duration.WithLabelValues(s.Database).Set(duration.Seconds())
	s.scrapeCount.Add(1)
	if fail {
		s.pgUp.Set(0)
		s.error.WithLabelValues(s.Database).Set(1)
		s.errorCount.Add(1)
	} else {
		s.pgUp.Set(1)
		s.error.WithLabelValues(s.Database).Set(0)
	}
	s.collectInternalMetrics(ch)
	log.Debugf("server [%s] scrapped in %v , fail=%v", s.Name(), duration, fail)
}

func (s *Server) collectInternalMetrics(ch chan<- prometheus.Metric) {
	if !s.disableCluster {
		// only primary server will report up & total & error
		ch <- s.pgUp
		ch <- s.lastScrapeTs
		ch <- s.scrapeCount
		ch <- s.errorCount
	}

	s.error.Collect(ch)
	s.duration.Collect(ch)
	s.metricCount.Collect(ch)
	s.queryError.Collect(ch)
	s.queryDuration.Collect(ch)
}

/**************************************************************\
* Server Creation
\**************************************************************/

// NewServer will check dsn, but not trying to connect
func NewServer(dsn string, opts ...ServerOpt) (s *Server, err error) {
	s = &Server{dsn: dsn}
	for _, opt := range opts {
		opt(s)
	}
	s.Database = parseDatname(dsn)
	if s.Database == "pgbouncer" {
		log.Infof("pgbouncer database detected, pgbouncer mode enabled")
		s.pgbouncerMode = true
	}

	s.setupInternalMetrics()
	return s, nil
}

// setupInternalMetrics will init internal metrics
func (s *Server) setupInternalMetrics() {
	pgnsp := "pg"
	if s.pgbouncerMode {
		pgnsp = "pgbouncer"
	}
	s.pgUp = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   pgnsp,
		Name:        "up",
		Help:        "last scrape was able to connect to the server: 1 for yes, 0 for no",
		ConstLabels: s.labels,
	})
	s.lastScrapeTs = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   pgnsp,
		Subsystem:   "exporter",
		Name:        "last_scrape_time",
		Help:        "time stamp of last scrape",
		ConstLabels: s.labels,
	})
	s.scrapeCount = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   pgnsp,
		Subsystem:   "exporter",
		Name:        "scrape_total_count",
		Help:        "times PostgresSQL was scraped for metrics.",
		ConstLabels: s.labels,
	})
	s.errorCount = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   pgnsp,
		Subsystem:   "exporter",
		Name:        "scrape_error_count",
		Help:        "times PostgresSQL was scraped for metrics and failed.",
		ConstLabels: s.labels,
	})
	s.error = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace:   pgnsp,
		Subsystem:   "exporter",
		Name:        "last_scrape_error",
		Help:        "whether last query is fail on this server",
		ConstLabels: s.labels,
	}, []string{"datname"})
	s.duration = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace:   pgnsp,
		Subsystem:   "exporter",
		Name:        "last_scrape_duration",
		Help:        "Duration of the last scrape of metrics from PostgresSQL.",
		ConstLabels: s.labels,
	}, []string{"datname"})
	s.metricCount = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace:   pgnsp,
		Subsystem:   "exporter",
		Name:        "query_metric_count",
		Help:        "how much metric been scrapped",
		ConstLabels: s.labels,
	}, []string{"datname", "query"})
	s.queryError = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace:   pgnsp,
		Subsystem:   "exporter",
		Name:        "query_errors",
		Help:        "whether query instance is error",
		ConstLabels: s.labels,
	}, []string{"datname", "query"})
	s.queryDuration = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace:   pgnsp,
		Subsystem:   "exporter",
		Name:        "query_duration",
		Help:        "duration of each query instance",
		ConstLabels: s.labels,
	}, []string{"datname", "query"})
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

// WithCachePolicy set cache parameter to server
func WithCachePolicy(disableCache bool) ServerOpt {
	return func(s *Server) {
		s.disableCache = disableCache
	}
}

// WithClusterQueryDisabled will marks server only execute query without cluster tag
func WithClusterQueryDisabled() ServerOpt {
	return func(s *Server) {
		s.disableCluster = true
	}
}

/**********************************************************************************************\
*                                        Exporter                                              *
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
	servers map[string]*Server // auto discovered peripheral servers
	queries map[string]*Query  // metrics query definition
}

// Close will close all underlying servers
func (e *Exporter) Close() {
	if e.server != nil {
		err := e.server.Close()
		if err != nil {
			log.Errorf("fail closing server %s: %s", e.server.Name(), err.Error())
		}
	}
	// close peripheral servers
	for _, server := range e.servers {
		err := server.Close()
		if err != nil {
			log.Errorf("fail closing server %s: %s", e.server.Name(), err.Error())
		}
	}
	log.Infof("pg exporter closed")
}

// Describe implement prometheus.Collector
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	e.server.Describe(ch)
}

// Collect implement prometheus.Collector
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	e.server.Collect(ch)
}

// Explain is just yet another wrapper of server.Explain
func (e *Exporter) Explain() string {
	return strings.Join(e.server.Explain(), "\n\n")
}

/**************************************************************\
* Exporter Creation
\**************************************************************/

// NewExporter construct a PG Exporter instance for given dsn
func NewExporter(dsn string, opts ...ExporterOpt) (e *Exporter, err error) {
	e = &Exporter{dsn: dsn}
	for _, opt := range opts {
		opt(e)
	}

	if e.queries, err = LoadConfig(e.configPath); err != nil {
		return nil, fmt.Errorf("fail loading config file %s: %w", e.configPath, err)
	}
	log.Debugf("exporter init with %d queries", len(e.queries))

	// note here the server is still not connected. it will trigger connecting when being scrapped
	if e.server, err = NewServer(
		dsn,
		WithConstLabel(e.constLabels),
		WithCachePolicy(e.disableCache),
	); err != nil {
		return nil, fmt.Errorf("fail creating server %s: %w", shadowDSN(dsn), err)
	}

	// register queries to server
	if err = e.server.Plan(e.queries); err != nil {
		return nil, fmt.Errorf("fail Plan queries for server %s: %w", e.server.Name(), err)
	}

	// TODO: discover peripheral servers and create servers
	return
}

// ExporterOpt configures Exporter
type ExporterOpt func(*Exporter)

// WithConfig add config path to Exporter
func WithConfig(configPath string) ExporterOpt {
	return func(e *Exporter) {
		e.configPath = configPath
	}
}

// WithConstLabels add const label to exporter. 0 length label returns nil
func WithConstLabels(s string) ExporterOpt {
	return func(e *Exporter) {
		e.constLabels = parseConstLabels(s)
	}
}

// WithCacheDisabled set cache param to exporter
func WithCacheDisabled(disableCache bool) ExporterOpt {
	return func(e *Exporter) {
		e.disableCache = disableCache
	}
}

/**********************************************************************************************\
*                                     Auxiliaries                                              *
\**********************************************************************************************/

// Compatible test whether query & server are compatible
// if not compatible, an error is returned for reason
func compatible(query *QueryInstance, server *Server) bool {
	// check version
	if server.Version != 0 {
		if query.MinVersion != 0 && server.Version < query.MinVersion {
			return false
		}
		if query.MaxVersion != 0 && server.Version >= query.MaxVersion { // exclude
			return false
		}
	}

	// check tags
	for _, tag := range query.Tags {
		switch tag {
		case "cluster":
			if server.disableCluster == true {
				return false
			}
		case "primary":
			if server.Recovery {
				return false
			}
		case "standby":
			if !server.Recovery {
				return false
			}
		case "pg_stat_statements":
			// TODO: NOT IMPLEMENT YET
		}
	}
	return true
}

// castString will force interface{} into float64
func castFloat64(t interface{}) float64 {
	switch v := t.(type) {
	case int64:
		return float64(v)
	case float64:
		return v
	case time.Time:
		return float64(v.Unix())
	case []byte:
		strV := string(v)
		result, err := strconv.ParseFloat(strV, 64)
		if err != nil {
			log.Warnf("fail casting []byte to float64: %v", t)
			return math.NaN()
		}
		return result
	case string:
		result, err := strconv.ParseFloat(v, 64)
		if err != nil {
			log.Warnf("fail casting string to float64: %v", t)
			return math.NaN()
		}
		return result
	case bool:
		if v {
			return 1.0
		}
		return 0.0
	case nil:
		return math.NaN()
	default:
		log.Warnf("fail casting unknown to float64: %v", t)
		return math.NaN()
	}
}

// castString will force interface{} into string
func castString(t interface{}) string {
	switch v := t.(type) {
	case int64:
		return fmt.Sprintf("%v", v)
	case float64:
		return fmt.Sprintf("%v", v)
	case time.Time:
		return fmt.Sprintf("%v", v.Unix())
	case nil:
		return ""
	case []byte:
		// Try and convert to string
		return string(v)
	case string:
		return v
	case bool:
		if v {
			return "true"
		}
		return "false"
	default:
		log.Warnf("fail casting unknown to string: %v", t)
		return ""
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

func parseDatname(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return ""
	}
	return strings.TrimLeft(u.Path, "/")
}

// GetPGURL will try to get dsn from different sources
func GetPGURL() (res string) {
	// priority: args > env  > env file path
	if *pgURL != "" {
		log.Infof("get target url %s from command line", shadowDSN(*pgURL))
		return *pgURL
	}
	if res = os.Getenv("PG_EXPORTER_URL"); res != "" {
		log.Infof("get target url %s from PG_EXPORTER_URL", shadowDSN(*pgURL))
		return res
	}
	if filename := os.Getenv("PG_EXPORTER_URL_FILE"); filename != "" {
		if fileContents, err := ioutil.ReadFile(filename); err != nil {
			log.Fatalf("PG_EXPORTER_URL_FILE=%s is specified, fail loading url: %s", err.Error())
			os.Exit(-1)
		} else {
			res = strings.TrimSpace(string(fileContents))
			log.Infof("get target url %s from PG_EXPORTER_URL_FILE", shadowDSN(res))
			return res
		}
	}
	log.Infof("target url not found, use default dsn: %s", defaultPGURL)
	return defaultPGURL
}

/**********************************************************************************************\
*                                        Main                                                  *
\**********************************************************************************************/
// Preset will parse parameters & validate dsn
func init() {
	kingpin.Version(fmt.Sprintf("postgres_exporter %s (built with %s)\n", Version, runtime.Version()))
	log.AddFlags(kingpin.CommandLine)
	kingpin.Parse()

	if err := log.Base().SetLevel(*logLevel); err != nil {
		log.Fatalf("fail setting log level to %s: %w", *logLevel, err)
		os.Exit(-1)
	}
}

func main() {
	pgurl := GetPGURL()
	exporter, err := NewExporter(
		pgurl,
		WithConfig(*configPath),
		WithConstLabels(*constLabels),
		WithCacheDisabled(*disableCache),
	)
	if err != nil {
		log.Fatalf("fail creating pg_exporter: %w", err)
		os.Exit(-3)
	}

	if *explainOnly {
		fmt.Println(exporter.Explain())
		os.Exit(0)
	}
	defer exporter.Close()
	prometheus.MustRegister(exporter)

	// run http
	http.Handle(*metricPath, promhttp.Handler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		_, _ = w.Write([]byte(`<html><head><title>PG Exporter</title></head><body><h1>PG Exporter</h1><p><a href='` + *metricPath + `'>Metrics</a></p></body></html>`))
	})
	http.HandleFunc("/explain", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		_, _ = w.Write([]byte(exporter.Explain()))
	})
	log.Infof("pg_exporter for %s start, listen on http://%s%s", shadowDSN(pgurl), *listenAddress, *metricPath)
	log.Fatal(http.ListenAndServe(*listenAddress, nil))
}
