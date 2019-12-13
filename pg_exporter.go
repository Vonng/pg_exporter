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

// Version 0.0.3
var Version = "0.0.3"

var defaultPGURL = "postgresql:///?sslmode=disable"

var (
	// exporter settings
	pgURL             = kingpin.Flag("url", "postgres connect url").String()
	configPath        = kingpin.Flag("config", "Path to config files").Default("./pg_exporter.yaml").Envar("PG_EXPORTER_CONFIG").String()
	constLabels       = kingpin.Flag("label", "Comma separated list label=value pair").Default("").Envar("PG_EXPORTER_LABELS").String()
	disableCache      = kingpin.Flag("disable-cache", "force not using cache").Default("false").Envar("PG_EXPORTER_CACHE").Bool()
	exporterNamespace = kingpin.Flag("namespace", "prefix of built-in metrics").Default("").Envar("PG_EXPORTER_NAMESPACE").String()

	// prometheus http
	listenAddress = kingpin.Flag("web.listen-address", "prometheus web server listen address").Default(":8848").Envar("PG_EXPORTER_LISTEN_ADDRESS").String()
	metricPath    = kingpin.Flag("web.telemetry-path", "Path under which to expose metrics.").Default("/metrics").Envar("PG_EXPORTER_TELEMETRY_PATH").String()

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
┃ Priority ┆ {{ .Priority }}
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
		return nil, fmt.Errorf("invalid config path: %s: %w", configPath, err)
	}
	if stat.IsDir() { // recursively iterate conf files if a dir is given
		files, err := ioutil.ReadDir(configPath)
		if err != nil {
			return nil, fmt.Errorf("fail reading config dir: %s: %w", configPath, err)
		}

		log.Debugf("load config from dir: %s", configPath)
		confFiles := make([]string, 0)
		for _, conf := range files {
			if !strings.HasSuffix(conf.Name(), ".yaml") && !conf.IsDir() { // depth = 1
				continue // skip non yaml files
			}
			confFiles = append(confFiles, path.Join(configPath, conf.Name()))
		}

		queries = make(map[string]*Query, 0)
		var queryCount, configCount int

		for index, confPath := range confFiles {
			if singleQueries, err := LoadConfig(confPath); err != nil {
				log.Warnf("skip config %s due to error: %s", confPath, err.Error())
			} else {
				configCount++
				cnt := 0
				for name, query := range singleQueries {
					// assign priority: assume you don't really provide 100+ conf files or put 100+ entry in a conf
					queryCount++
					if query.Priority == 0 {
						query.Priority = 10000 - index*100 - cnt
					}
					queries[name] = query // so the later one will overwrite former one
				}
			}
		}
		log.Debugf("load %d of %d queries from %d config files", len(queries), queryCount, configCount)
		return queries, nil
	}

	// single file case: recursive exit condition
	content, err := ioutil.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("fail reading config file %s: %w", configPath, err)
	}
	queries, err = ParseConfig(content)
	if err != nil {
		return nil, err
	}
	for _, q := range queries {
		q.Path = stat.Name()
	}
	log.Debugf("load %d queries from %s, ", len(queries), configPath)
	return queries, nil

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
	err         error
	cacheHit    bool // indicate last scrape was served from cache or real execution

	lastScrape  time.Time // server's scrape start time
	scrapeBegin time.Time // real execution begin
	scrapeDone  time.Time // execution complete time
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
	q.lock.Lock()
	defer q.lock.Unlock()
	q.sendDescriptors(ch)
}

// Collect implement prometheus.Collector
func (q *QueryInstance) Collect(ch chan<- prometheus.Metric) {
	q.lock.Lock()
	defer q.lock.Unlock()
	if q.cacheExpired() || q.Server.DisableCache {
		q.execute()
		q.cacheHit = false
	} else {
		q.cacheHit = true
	}
	// if execution failed, the cache is already reset
	q.sendMetrics(ch)
}

// ResultSize report last scrapped metric count
func (q *QueryInstance) ResultSize() int {
	q.lock.RLock()
	q.lock.RUnlock()
	return len(q.result)
}

// Error wraps query error
func (q *QueryInstance) Error() error {
	q.lock.RLock()
	q.lock.RUnlock()
	return q.err
}

// Duration returns last scrape duration in float64 seconds
func (q *QueryInstance) Duration() float64 {
	q.lock.RLock()
	q.lock.RUnlock()
	return q.scrapeDone.Sub(q.scrapeBegin).Seconds()
}

// CacheHit report whether last scrape was serve from cache
func (q *QueryInstance) CacheHit() bool {
	q.lock.RLock()
	q.lock.RUnlock()
	return q.cacheHit
}

// execute will run this query to registered server, result and err are registered
func (q *QueryInstance) execute() {
	q.result = q.result[:0] // reset cache
	q.scrapeBegin = time.Now()

	// Server must be inited before using
	rows, err := q.Server.Query(q.SQL)
	if err != nil {
		q.err = fmt.Errorf("query %s failed: %w", q.Name, err)
		q.scrapeDone = time.Now()
		return
	}
	defer rows.Close() // nolint: errcheck

	// get result metadata for dynamic name lookup, prepare line cache
	columnNames, err := rows.Columns()
	if err != nil {
		q.err = fmt.Errorf("query %s fail retriving rows meta: %w", q.Name, err)
		q.scrapeDone = time.Now()
		return
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
			q.err = fmt.Errorf("fail scanning rows: %w", err)
			q.scrapeDone = time.Now()
			return
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
				log.Warnf("missing metric column %s.%s in result", q.Name, metricName)
			}
		}
	}
	q.err = nil
	q.scrapeDone = time.Now()
	q.lastScrape = q.Server.scrapeBegin // align cache by using server's last scrape time
	log.Debugf("query %s cache expired, execute duration %v, %d metrics scrapped",
		q.Name, q.scrapeDone.Sub(q.scrapeBegin), len(q.result))
	return
}

/**************************************************************\
* Query Instance Auxiliary
\**************************************************************/

// makeDescMap will generate descriptor map from Query
func (q *QueryInstance) makeDescMap() {
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
	for _, desc := range q.descriptors {
		ch <- desc
	}
}

// cacheExpired report whether this instance needs actual execution
// Note you have to using Server.scrapeBegin as "now", and set that timestamp as
func (q *QueryInstance) cacheExpired() bool {
	if q.Server.scrapeBegin.Sub(q.lastScrape) > time.Duration(q.TTL*float64(time.Second)) {
		return true
	}
	return false
}

func (q *QueryInstance) cacheTTL() float64 {
	return q.TTL - q.Server.scrapeBegin.Sub(q.lastScrape).Seconds()
}

// sendMetrics will send cached result to ch
func (q *QueryInstance) sendMetrics(ch chan<- prometheus.Metric) {
	for _, metric := range q.result {
		ch <- metric
	}
}

/**********************************************************************************************\
*                                       Server                                                 *
\**********************************************************************************************/

// Server represent a postgres connection, with additional fact, conf, runtime info
type Server struct {
	*sql.DB                   // database instance
	dsn          string       // data source name
	lock         sync.RWMutex // scrape lock avoid concurrent call
	err          error
	beforeScrape func(s *Server) error

	// postgres fact gather from server
	UP             bool
	Version        int      // pg server version num
	Database       string   // database name of current server connection
	DatabaseNames  []string // all available database in target cluster
	ExtensionNames []string // all available database in target cluster
	Recovery       bool     // is server recovering? (slave|standby)

	PgbouncerMode  bool // indicate it is a pgbouncer server
	DisableCache   bool // force executing, ignoring caching policy
	DisableCluster bool // cluster server will not run on this server
	Planned        bool

	// query
	//queries        []string          // query sequence
	queries   map[string]*Query // original unfiltered query instance in pirority order
	instances []*QueryInstance  // query instance
	labels    prometheus.Labels // constant label that inject into metrics

	// internal stats
	scrapeBegin time.Time // server level scrape begin
	scrapeDone  time.Time // server last scrape done
	errorCount  float64
	totalCount  float64
	totalTime   float64

	queryCacheTTL          map[string]float64
	queryScrapeTotalCount  map[string]float64
	queryScrapeHitCount    map[string]float64
	queryScrapeErrorCount  map[string]float64
	queryScrapeMetricCount map[string]float64
	queryScrapeDuration    map[string]float64
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

// PgbouncerPrecheck checks pgbouncer connection before scrape
func PgbouncerPrecheck(s *Server) (err error) {
	if s.DB == nil { // if db is not initialized, create a new DB
		if s.DB, err = sql.Open("postgres", s.dsn); err != nil {
			s.UP = false
			return
		}
		s.DB.SetMaxIdleConns(1)
		s.DB.SetMaxOpenConns(1)
	}

	// TODO: since pgbouncer 1.12- using NOTICE to tell version, we just leave it blank here
	if _, err = s.DB.Exec(`SHOW VERSION;`); err != nil {
		return
	}
	return nil
}

// PostgresPrecheck checks postgres connection before scrape
func PostgresPrecheck(s *Server) (err error) {
	if s.DB == nil { // if db is not initialized, create a new DB
		if s.DB, err = sql.Open("postgres", s.dsn); err != nil {
			s.UP = false
			return
		}
		s.DB.SetMaxIdleConns(1)
		s.DB.SetMaxOpenConns(1)
	}

	// retrieve version info
	var version int
	if err = s.DB.QueryRow(`SHOW server_version_num;`).Scan(&version); err != nil {
		return fmt.Errorf("fail fetching server version: %w", err)
	}
	// fact change triggers a new planning
	if s.Version != version {
		log.Infof("server [%s] version changed: from [%d] to [%d]", s.Name(), s.Version, version)
		s.Planned = false
	}
	s.Version = version

	var recovery bool
	var datname string
	//var extnames, datnames []string

	if err = s.DB.QueryRow(`SELECT current_catalog, pg_is_in_recovery();`).Scan(&datname, &recovery); err != nil {
		return fmt.Errorf("fail fetching server version: %w", err)
	}
	if s.Recovery != recovery {
		log.Infof("server [%s] recovery status changed: from [%v] to [%v]", s.Name(), s.Recovery, recovery)
		s.Planned = false
	}
	s.Recovery = recovery
	if s.Database != datname {
		log.Infof("server [%s] datname changed: from [%s] to [%s]", s.Name(), s.Database, datname)
		s.Planned = false
	}
	s.Database = datname

	return nil
}

// Plan will register queries that option fit server's fact
func (s *Server) Plan(queries ...*Query) {
	if len(queries) > 0 {
		// if queries are explicitly given to plan, use it anyway
		newQueries := make(map[string]*Query, 0)
		for _, q := range queries {
			newQueries[q.Name] = q
		}
		s.queries = newQueries
	}

	// only register those matches server's fact
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
		return instances[i].Priority > instances[j].Priority
	})
	s.instances = instances

	// reset statistics
	s.ResetStats()

	s.Planned = true
	log.Infof("server %s planned with %d queries, %d installed, %d discarded, installed: %s , discarded: %s",
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

	// check tags
	if query.HasTag("cluster") && s.DisableCluster {
		return false, fmt.Sprintf("cluster level query %s will not run on non-cluster server %v", query.Name, s.Name())
	}
	if query.HasTag("primary") && s.Recovery {
		return false, fmt.Sprintf("primary-only query %s will not run on standby server %v", query.Name, s.Name())
	}
	if query.HasTag("standby") && !s.Recovery {
		return false, fmt.Sprintf("standby-only query %s will not run on primary server %v", query.Name, s.Name())
	}
	if query.HasTag("pg_stat_statements") {
		// TODO: check extension exist...
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
	// we just run Collect when Describe is invoked, therefore runtime planning is done before describe
	metricCh := make(chan prometheus.Metric)
	doneCh := make(chan struct{})
	go func() {
		cnt := 0
		for range metricCh {
			//ch <- m.Desc()
			cnt++
		}
		log.Infof("describe server %s: %d metrics collected", s.Name(), cnt)
		close(doneCh)
	}()
	s.Collect(metricCh)
	close(metricCh)
	<-doneCh

	// then we just sending are registed descriptor
	for _, instance := range s.instances {
		instance.Describe(ch)
	}
}

// Collect implement prometheus.Collector interface
func (s *Server) Collect(ch chan<- prometheus.Metric) {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.scrapeBegin = time.Now() // This ts is used for cache expiration check

	// precheck hook
	if s.beforeScrape != nil {
		if s.err = s.beforeScrape(s); s.err != nil {
			log.Errorf("fail establishing connection to %s: %s", s.Name(), s.err.Error())
			goto final
		}
	}

	if !s.Planned {
		s.Plan()
	}

	for _, query := range s.instances {
		query.Collect(ch)
		s.queryCacheTTL[query.Name] = query.cacheTTL()
		s.queryScrapeTotalCount[query.Name]++
		s.queryScrapeMetricCount[query.Name] = float64(query.ResultSize())
		s.queryScrapeDuration[query.Name] = query.Duration()
		if query.Error() != nil {
			s.queryScrapeErrorCount[query.Name]++
			if query.SkipErrors { // skip this error according to config
				log.Warnf("query %s error skipped: %s", query.Name, query.Error())
				continue
			} else { // treat as fatal error
				log.Errorf("query %s error: %s", query.Name, query.Error())
				s.err = query.Error()
				goto final
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

// Duration returns last scrape duration in float64 seconds
func (s *Server) Duration() float64 {
	s.lock.RLock()
	s.lock.RUnlock()
	return s.scrapeDone.Sub(s.scrapeBegin).Seconds()
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
func WithClusterQueryDisabled() ServerOpt {
	return func(s *Server) {
		s.DisableCluster = true
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
	pgbouncerMode     bool              // is primary server a pgbouncer ?
	excludedDatabases []string          // excluded database for auto discovery
	constLabels       prometheus.Labels // prometheus const k=v labels
	namespace         string

	lock    sync.RWMutex
	server  *Server            // primary server
	servers map[string]*Server // auto discovered peripheral servers
	queries map[string]*Query  // metrics query definition

	// internal stats
	scrapeBegin time.Time // server level scrape begin
	scrapeDone  time.Time // server last scrape done
	errorCount  float64
	totalCount  float64

	// internal metrics: global, exporter, server, query
	up               prometheus.Gauge   // cluster level: primary target server is alive
	version          prometheus.Gauge   // cluster level: postgres main server version num
	recovery         prometheus.Gauge   // cluster level: postgres is in recovery ?
	exporterUp       prometheus.Gauge   // exporter level: always set ot 1
	lastScrapeTime   prometheus.Gauge   // exporter level: last scrape timestamp
	scrapeDuration   prometheus.Gauge   // exporter level: seconds spend on scrape
	scrapeTotalCount prometheus.Counter // exporter level: total scrape count of this server
	scrapeErrorCount prometheus.Counter // exporter level: error scrape count

	serverScrapeDuration     *prometheus.GaugeVec // {datname} database level: how much time spend on server scrape?
	serverScrapeTotalSeconds *prometheus.GaugeVec // {datname} database level: how much time spend on server scrape?
	serverScrapeTotalCount   *prometheus.GaugeVec // {datname} database level how many metrics scrapped from server
	serverScrapeErrorCount   *prometheus.GaugeVec // {datname} database level: how many error occurs when scrapping server

	queryCacheTTL          *prometheus.GaugeVec // {datname,query} query cache ttl
	queryScrapeTotalCount  *prometheus.GaugeVec // {datname,query} query level: how many errors the query triggers?
	queryScrapeErrorCount  *prometheus.GaugeVec // {datname,query} query level: how many errors the query triggers?
	queryScrapeDuration    *prometheus.GaugeVec // {datname,query} query level: how many seconds the query spends?
	queryScrapeMetricCount *prometheus.GaugeVec // {datname,query} query level: how many metrics the query returns?
	queryScrapeHitCount    *prometheus.GaugeVec // {datname,query} query level: how many errors the query triggers?

}

// Describe implement prometheus.Collector
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	e.server.Describe(ch)
}

// Collect implement prometheus.Collector
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	e.lock.Lock()
	defer e.lock.Unlock()
	e.scrapeTotalCount.Add(1)

	// TODO: multi-server
	e.scrapeBegin = time.Now()
	s := e.server
	s.Collect(ch)
	e.scrapeDone = time.Now()

	e.lastScrapeTime.Set(float64(e.scrapeDone.Unix()))
	e.scrapeDuration.Set(e.scrapeDone.Sub(e.scrapeBegin).Seconds())
	e.version.Set(float64(s.Version))
	if s.UP {
		e.up.Set(1)
		if s.Recovery {
			e.recovery.Set(1)
		} else {
			e.recovery.Set(0)
		}
	} else {
		e.up.Set(0)
		e.scrapeErrorCount.Add(1)
	}

	e.collectServerMetrics(s)
	e.collectInternalMetrics(ch)
}

func (e *Exporter) collectServerMetrics(s *Server) {
	e.serverScrapeDuration.Reset()
	e.serverScrapeTotalSeconds.Reset()
	e.serverScrapeTotalCount.Reset()
	e.serverScrapeErrorCount.Reset()
	e.queryCacheTTL.Reset()
	e.queryScrapeTotalCount.Reset()
	e.queryScrapeErrorCount.Reset()
	e.queryScrapeDuration.Reset()
	e.queryScrapeMetricCount.Reset()
	e.queryScrapeHitCount.Reset()

	e.serverScrapeDuration.WithLabelValues(s.Database).Set(s.Duration())
	e.serverScrapeTotalSeconds.WithLabelValues(s.Database).Set(s.totalTime)
	e.serverScrapeTotalCount.WithLabelValues(s.Database).Set(s.totalCount)
	if s.Error() != nil {
		e.serverScrapeErrorCount.WithLabelValues(s.Database).Add(1)
	}

	for queryName, counter := range s.queryCacheTTL {
		e.queryCacheTTL.WithLabelValues(s.Database, queryName).Set(counter)
	}
	for queryName, counter := range s.queryScrapeTotalCount {
		e.queryScrapeTotalCount.WithLabelValues(s.Database, queryName).Set(counter)
	}
	for queryName, counter := range s.queryScrapeHitCount {
		e.queryScrapeHitCount.WithLabelValues(s.Database, queryName).Set(counter)
	}
	for queryName, counter := range s.queryScrapeErrorCount {
		e.queryScrapeErrorCount.WithLabelValues(s.Database, queryName).Set(counter)
	}
	for queryName, counter := range s.queryScrapeMetricCount {
		e.queryScrapeMetricCount.WithLabelValues(s.Database, queryName).Set(counter)
	}
	for queryName, counter := range s.queryScrapeDuration {
		e.queryScrapeDuration.WithLabelValues(s.Database, queryName).Set(counter)
	}
}

// Explain is just yet another wrapper of server.Explain
func (e *Exporter) Explain() string {
	return strings.Join(e.server.Explain(), "\n\n")
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

// setupInternalMetrics will init internal metrics
func (e *Exporter) setupInternalMetrics() {
	if e.namespace == "" {
		if e.pgbouncerMode {
			e.namespace = "pgbouncer"
		} else {
			e.namespace = "pg"
		}
	}

	// major fact
	e.up = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: e.namespace, ConstLabels: e.constLabels,
		Name: "up", Help: "last scrape was able to connect to the server: 1 for yes, 0 for no",
	})
	e.version = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: e.namespace, ConstLabels: e.constLabels,
		Name: "version", Help: "server version number",
	})
	e.recovery = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: e.namespace, ConstLabels: e.constLabels,
		Name: "in_recovery", Help: "server is in recovery mode? 1 for yes 0 for no",
	})

	// exporter level metrics
	e.exporterUp = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: e.namespace, ConstLabels: e.constLabels,
		Subsystem: "exporter", Name: "up", Help: "always be 1 if your could retrieve metrics",
	})
	e.scrapeTotalCount = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: e.namespace, ConstLabels: e.constLabels,
		Subsystem: "exporter", Name: "scrape_total_count", Help: "times exporter was scraped for metrics",
	})
	e.scrapeErrorCount = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: e.namespace, ConstLabels: e.constLabels,
		Subsystem: "exporter", Name: "scrape_error_count", Help: "times exporter was scraped for metrics and failed",
	})
	e.scrapeDuration = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: e.namespace, ConstLabels: e.constLabels,
		Subsystem: "exporter", Name: "scrape_duration", Help: "seconds exporter spending on scrapping",
	})
	e.lastScrapeTime = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: e.namespace, ConstLabels: e.constLabels,
		Subsystem: "exporter", Name: "last_scrape_time", Help: "seconds exporter spending on scrapping",
	})

	// exporter level metrics
	e.serverScrapeDuration = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: e.namespace, ConstLabels: e.constLabels,
		Subsystem: "exporter_server", Name: "scrape_duration", Help: "seconds exporter server spending on scrapping",
	}, []string{"datname"})
	e.serverScrapeTotalSeconds = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: e.namespace, ConstLabels: e.constLabels,
		Subsystem: "exporter_server", Name: "scrape_total_seconds", Help: "seconds exporter server spending on scrapping",
	}, []string{"datname"})
	e.serverScrapeTotalCount = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: e.namespace, ConstLabels: e.constLabels,
		Subsystem: "exporter_server", Name: "scrape_total_count", Help: "times exporter server was scraped for metrics",
	}, []string{"datname"})
	e.serverScrapeErrorCount = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: e.namespace, ConstLabels: e.constLabels,
		Subsystem: "exporter_server", Name: "scrape_error_count", Help: "times exporter server was scraped for metrics and failed",
	}, []string{"datname"})

	// query level metrics
	e.queryCacheTTL = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: e.namespace, ConstLabels: e.constLabels,
		Subsystem: "exporter_query", Name: "cache_ttl", Help: "times to live of query cache",
	}, []string{"datname", "query"})
	e.queryScrapeTotalCount = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: e.namespace, ConstLabels: e.constLabels,
		Subsystem: "exporter_query", Name: "scrape_total_count", Help: "times exporter server was scraped for metrics",
	}, []string{"datname", "query"})
	e.queryScrapeErrorCount = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: e.namespace, ConstLabels: e.constLabels,
		Subsystem: "exporter_query", Name: "scrape_error_count", Help: "times the query failed",
	}, []string{"datname", "query"})
	e.queryScrapeDuration = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: e.namespace, ConstLabels: e.constLabels,
		Subsystem: "exporter_query", Name: "scrape_duration", Help: "seconds query spending on scrapping",
	}, []string{"datname", "query"})
	e.queryScrapeMetricCount = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: e.namespace, ConstLabels: e.constLabels,
		Subsystem: "exporter_query", Name: "scrape_metric_count", Help: "numbers of metrics been scrapped from this query",
	}, []string{"datname", "query"})
	e.queryScrapeHitCount = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: e.namespace, ConstLabels: e.constLabels,
		Subsystem: "exporter_query", Name: "scrape_hit_count", Help: "numbers  been scrapped from this query",
	}, []string{"datname", "query"})

	e.exporterUp.Set(1) // always be true
}

func (e *Exporter) collectInternalMetrics(ch chan<- prometheus.Metric) {
	ch <- e.up
	ch <- e.version
	ch <- e.recovery

	ch <- e.exporterUp
	ch <- e.lastScrapeTime
	ch <- e.scrapeTotalCount
	ch <- e.scrapeErrorCount
	ch <- e.scrapeDuration

	e.serverScrapeDuration.Collect(ch)
	e.serverScrapeTotalSeconds.Collect(ch)
	e.serverScrapeTotalCount.Collect(ch)
	e.serverScrapeErrorCount.Collect(ch)

	e.queryCacheTTL.Collect(ch)
	e.queryScrapeTotalCount.Collect(ch)
	e.queryScrapeErrorCount.Collect(ch)
	e.queryScrapeDuration.Collect(ch)
	e.queryScrapeMetricCount.Collect(ch)
	e.queryScrapeHitCount.Collect(ch)
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
	e.server = NewServer(
		dsn,
		WithQueries(e.queries),
		WithConstLabel(e.constLabels),
		WithCachePolicy(e.disableCache),
	)
	e.pgbouncerMode = e.server.PgbouncerMode
	e.setupInternalMetrics()
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

func WithNamespace(namespace string) ExporterOpt {
	return func(e *Exporter) {
		e.namespace = namespace
	}
}

/**********************************************************************************************\
*                                     Auxiliaries                                              *
\**********************************************************************************************/

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

func dumpMap(d map[string]float64) string {
	var buf bytes.Buffer
	buf.WriteByte('[')
	for k, v := range d {
		buf.WriteString(k)
		buf.WriteByte(':')
		buf.WriteString(strconv.Itoa(int(v)))
		buf.WriteString(", ")
	}
	buf.WriteByte(']')
	return buf.String()
}

// RetrieveTargetURL retrieve pg target url from different sources
func RetrieveTargetURL() (res string) {
	// priority: cli-args > env  > env file path
	if *pgURL != "" {
		log.Infof("retrieve target url %s from command line", shadowDSN(*pgURL))
		return *pgURL
	}
	if res = os.Getenv("PG_EXPORTER_URL"); res != "" {
		log.Infof("retrieve target url %s from PG_EXPORTER_URL", shadowDSN(*pgURL))
		return res
	}
	if filename := os.Getenv("PG_EXPORTER_URL_FILE"); filename != "" {
		if fileContents, err := ioutil.ReadFile(filename); err != nil {
			log.Fatalf("PG_EXPORTER_URL_FILE=%s is specified, fail loading url, exit", err.Error())
			os.Exit(-1)
		} else {
			res = strings.TrimSpace(string(fileContents))
			log.Infof("retrieve target url %s from PG_EXPORTER_URL_FILE", shadowDSN(res))
			return res
		}
	}
	log.Warnf("fail retrieving target url, fallback on default url: %s", defaultPGURL)
	return defaultPGURL
}

/**********************************************************************************************\
*                                        Main                                                  *
\**********************************************************************************************/

// parse parameters & retrieve dsn
func init() {
	kingpin.Version(fmt.Sprintf("postgres_exporter %s (built with %s)\n", Version, runtime.Version()))
	log.AddFlags(kingpin.CommandLine)
	kingpin.Parse()
	log.Debugf("init pg_exporter, configPath=%v constLabels=%v, disableCache=%v, listenAdress=%v metricPath=%v",
		*configPath, *constLabels, *disableCache, *listenAddress, *metricPath)
	*pgURL = RetrieveTargetURL()
}

// Run pg exporter
func Run() {
	exporter, err := NewExporter(
		*pgURL,
		WithConfig(*configPath),
		WithConstLabels(*constLabels),
		WithCacheDisabled(*disableCache),
		WithNamespace(*exporterNamespace),
	)
	if err != nil {
		log.Fatalf("fail creating pg_exporter: %w", err)
		os.Exit(-3)
	}

	if *explainOnly {
		fmt.Println(exporter.Explain())
		os.Exit(0)
	}

	prometheus.MustRegister(exporter)
	defer exporter.Close()

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
	log.Infof("pg_exporter for %s start, listen on http://%s%s", shadowDSN(*pgURL), *listenAddress, *metricPath)
	log.Fatal(http.ListenAndServe(*listenAddress, nil))
}

func main() {
	Run()
}
