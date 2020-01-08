package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"github.com/lib/pq"
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

// Version is read by make build procedure
var Version = "0.0.5"

var defaultPGURL = "postgresql:///?sslmode=disable"

var (
	// exporter settings
	pgURL             = kingpin.Flag("url", "postgres connect url").String()
	configPath        = kingpin.Flag("config", "Path to config files").Default("./pg_exporter.yaml").Envar("PG_EXPORTER_CONFIG").String()
	constLabels       = kingpin.Flag("label", "Comma separated list label=value pair").Default("").Envar("PG_EXPORTER_LABEL").String()
	serverTags        = kingpin.Flag("tag", "Comma separated list of server tag").Default("").Envar("PG_EXPORTER_TAG").String()
	disableCache      = kingpin.Flag("disable-cache", "force not using cache").Default("false").Envar("PG_EXPORTER_CACHE").Bool()
	autoDiscovery     = kingpin.Flag("auto-discovery", "scrape all database for given cluster").Default("false").Envar("PG_EXPORTER_AUTO_DISCOVERY").Bool()
	excludeDatabase   = kingpin.Flag("exclude-database", "excluded databases when enabling auto-discovery").Default("postgres,template0,template1").Envar("PG_EXPORTER_EXCLUDE_DATABASE").String()
	exporterNamespace = kingpin.Flag("namespace", "prefix of built-in metrics (pg|pgbouncer) by default").Default("").Envar("PG_EXPORTER_NAMESPACE").String()
	failFast          = kingpin.Flag("fail-fast", "fail fast instead of waiting during start-up").Envar("PG_EXPORTER_FAIL_FAST").Default("false").Bool()

	// prometheus http
	listenAddress = kingpin.Flag("web.listen-address", "prometheus web server listen address").Default(":9630").Envar("PG_EXPORTER_LISTEN_ADDRESS").String()
	metricPath    = kingpin.Flag("web.telemetry-path", "Path under which to expose metrics.").Default("/metrics").Envar("PG_EXPORTER_TELEMETRY_PATH").String()

	// action
	dryRun      = kingpin.Flag("dry-run", "dry run and explain raw conf").Default("false").Bool()
	explainOnly = kingpin.Flag("explain", "explain server planned queries").Default("false").Bool()
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

// ColumnUsage determine how to use query result column
var ColumnUsage = map[string]bool{
	DISCARD: false,
	LABEL:   false,
	COUNTER: true,
	GAUGE:   true,
}

// Column holds the metadata of query result
type Column struct {
	Name   string `yaml:"name"`
	Desc   string `yaml:"description"`
	Usage  string `yaml:"usage"`
	Rename string `yaml:"rename"`
}

// PrometheusValueType returns column's corresponding prometheus value type
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

// String turns column into a one-line text representation
func (c *Column) String() string {
	return fmt.Sprintf("%-8s %-20s %s", c.Usage, c.Name, c.Desc)
}

/**********************************************************************************************\
*                                       Query                                                  *
\**********************************************************************************************/

// Query hold the information of how to fetch metric and parse them
type Query struct {
	Name   string `yaml:"name"`  // actual query name, used as metric prefix
	Desc   string `yaml:"desc"`  // description of this metric query
	SQL    string `yaml:"query"` // SQL command to fetch metrics
	Branch string // branch name, top layer key of config file

	// control query behaviour
	Tags       []string `yaml:"tags"`        // tags are used for execution control
	TTL        float64  `yaml:"ttl"`         // caching ttl in seconds
	Timeout    float64  `yaml:"timeout"`     // query execution timeout in seconds
	Priority   int      `yaml:"priority"`    // execution priority, from 1 to 999
	MinVersion int      `yaml:"min_version"` // minimal supported version, include
	MaxVersion int      `yaml:"max_version"` // maximal supported version, not include
	Fatal      bool     `yaml:"fatal"`       // if query marked fatal fail, entire scrape will fail

	Metrics []map[string]*Column `yaml:"metrics"` // metric definition list

	// metrics parsing auxiliaries
	Path        string             // where am I from ?
	Columns     map[string]*Column // column map
	ColumnNames []string           // column names in origin orders
	LabelNames  []string           // column (name) that used as label, sequences matters
	MetricNames []string           // column (name) that used as metric
}

var queryTemplate, _ = template.New("Query").Parse(`
#  ┏━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
#  ┃ {{ .Name }}{{ if ne .Name .Branch}}.{{ .Branch }}{{end}}
#  ┃ {{ .Desc }}
#  ┣┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈
#  ┃ Tags     ┆ {{ .Tags }}
#  ┃ TTL      ┆ {{ .TTL }}
#  ┃ Priority ┆ {{ .Priority }}
#  ┃ Timeout  ┆ {{ .TimeoutDuration }}
#  ┃ Fatal    ┆ {{ .Fatal }}
#  ┃ Version  ┆ {{if ne .MinVersion 0}}{{ .MinVersion }}{{else}}lower{{end}} ~ {{if ne .MaxVersion 0}}{{ .MaxVersion }}{{else}}higher{{end}}
#  ┃ Source   ┆ {{.Path }}
#  ┣┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈
{{range .ColumnList}}#  ┃ {{.}}
{{end}}#  ┣┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈
{{range .MetricList}}#  ┃ {{.}}
{{end}}#  ┗┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈
#  ┃ {{.TemplateSQL}}
#  ┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━`)

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
// since only few tags is provided, we don't really need a map here
func (q *Query) HasTag(tag string) bool {
	for _, t := range q.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// ColumnList return ordered column list
func (q *Query) ColumnList() (res []*Column) {
	res = make([]*Column, len(q.ColumnNames))
	for i, colName := range q.ColumnNames {
		res[i] = q.Columns[colName]
	}
	return
}

// MetricList returns a list of metric generated by this query
func (q *Query) MetricList() (res []string) {
	labelNames := make([]string, len(q.LabelNames))
	for i, labelName := range q.LabelNames {
		labelColumn := q.Columns[labelName]
		if labelColumn.Rename != "" {
			labelNames[i] = labelColumn.Rename
		} else {
			labelNames[i] = labelColumn.Name
		}
	}
	labels := strings.Join(labelNames, ",")

	res = make([]string, len(q.MetricNames))
	for i, metricName := range q.MetricNames {
		column := q.Columns[metricName]
		name := column.Name
		if column.Rename != "" {
			name = column.Rename
		}
		res[i] = fmt.Sprintf("%s_%s{%s}", q.Name, name, labels)
	}
	return
}

func (q *Query) TemplateSQL() string {
	return strings.Replace(q.SQL, "\n", "\n#  ┃ ", -1)
}

func (q *Query) TimeoutDuration() time.Duration {
	return time.Duration(float64(time.Second) * q.Timeout)
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
				allColumns = append(allColumns, column.Name)
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

		// make global config map and assign priority according to config file alphabetic orders
		// priority is an integer range from 1 to 999, where 1 - 99 is reserved for user
		queries = make(map[string]*Query, 0)
		var queryCount, configCount int
		for _, confPath := range confFiles {
			if singleQueries, err := LoadConfig(confPath); err != nil {
				log.Warnf("skip config %s due to error: %s", confPath, err.Error())
			} else {
				configCount++
				for name, query := range singleQueries {
					queryCount++
					if query.Priority == 0 { // set to config rank if not manually set
						query.Priority = 100 + configCount
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
	for branch, q := range queries {
		q.Path = stat.Name()
		q.Branch = branch
		// if timeout is not set, set to 100ms by default
		// if timeout is set to a neg number, set to 0 so it's actually disabled
		if q.Timeout == 0 {
			q.Timeout = 0.1
		}
		if q.Timeout < 0 {
			q.Timeout = 0
		}
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

	// if timeout is provided, use context
	var rows *sql.Rows
	var err error
	if q.Timeout != 0 {
		ctx, _ := context.WithTimeout(context.Background(), q.TimeoutDuration())
		rows, err = q.Server.QueryContext(ctx, q.SQL)
	} else {
		rows, err = q.Server.Query(q.SQL)
	}

	if err != nil {
		q.scrapeDone = time.Now()
		// timeout or query error
		if strings.Contains(err.Error(), "canceling statement due to user request") {
			q.err = fmt.Errorf("query %s timeout, duration: %v, limit: %v", q.Name, q.Duration(), q.TimeoutDuration())
		} else {
			q.err = fmt.Errorf("query %s failed: %w", q.Name, err)
		}
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
	*sql.DB              // database instance
	dsn     string       // data source name
	lock    sync.RWMutex // server scrape lock
	err     error        // last error

	// hooks
	beforeScrape     func(s *Server) error        // hook: execute before scrape
	onDatabaseChange func(change map[string]bool) // hook: invoke when database list is changed

	// postgres fact gather from server
	UP         bool            // indicate whether target server is connectable
	Version    int             // pg server version num
	Database   string          // database name of current server connection
	Databases  map[string]bool // all available database in target cluster
	dblistLock sync.Mutex      // lock when access Databases map
	Namespaces map[string]bool // all available schema in target cluster
	Extensions map[string]bool // all available extension in target cluster
	Recovery   bool            // is server in recovering?

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
			return
		}
		s.DB.SetMaxIdleConns(1)
		s.DB.SetMaxOpenConns(1)
	}

	// retrieve version info
	var version int
	ctx, _ := context.WithTimeout(context.Background(), time.Second)
	if err = s.DB.QueryRowContext(ctx, `SHOW server_version_num;`).Scan(&version); err != nil {
		return fmt.Errorf("fail fetching server version: %w", err)
	}
	// fact change triggers a new planning
	if s.Version != version {
		log.Infof("server %s version changed: from [%d] to [%d]", s.Name(), s.Version, version)
		s.Planned = false
	}
	s.Version = version

	var recovery bool
	var datname string
	var databases, namespaces, extensions []string

	precheckSQL := `SELECT current_catalog, pg_is_in_recovery(),       
	(SELECT array_agg(datname) AS databases FROM pg_database),
	(SELECT array_agg(nspname) AS namespaces FROM pg_namespace),
	(SELECT array_agg(extname) AS extensions FROM pg_extension);`
	ctx, _ = context.WithTimeout(context.Background(), time.Second)
	if err = s.DB.QueryRowContext(ctx, precheckSQL).Scan(&datname, &recovery, pq.Array(&databases), pq.Array(&namespaces), pq.Array(&extensions));
		err != nil {
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
			log.Debugf("server %s found new database %s", s.Name(), dbname)
			changes[dbname] = true
		}
	}
	// if old db is not found in new db list, add a change entry [OldDBName:false]
	for dbname, _ := range s.Databases {
		if _, found := newDBList[dbname]; !found {
			log.Debugf("server %s found vanished database %s", s.Name(), dbname)
			changes[dbname] = false
		}
	}
	// invoke hook if there are changes on database list
	if len(changes) > 0 && s.onDatabaseChange != nil {
		log.Debugf("auto discovery database list change : %v", changes)
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

	// check query side tags
	for _, tag := range query.Tags {
		// check extension is installed on target database
		if strings.HasPrefix(tag, "extension:") {
			if _, found := s.Extensions[strings.TrimPrefix(tag, "extension:")]; !found {
				return false, fmt.Sprintf("server does not have %s", tag)
			}
			continue
		}

		// check schema exist on target database
		if strings.HasPrefix(tag, "schema:") {
			if _, found := s.Namespaces[strings.TrimPrefix(tag, "schema:")]; !found {
				return false, fmt.Sprintf("server does not have %s", tag)
			}
			continue
		}

		// check schema exist on target database
		if strings.HasPrefix(tag, "dbname:") {
			if s.Database != strings.TrimPrefix(tag, "dbname:") {
				return false, fmt.Sprintf("server %s does not match with query tag %s", s.Database, tag)
			}
			continue
		}

		// check server does not have given tag
		if strings.HasPrefix(tag, "not:") {
			if negTag := strings.TrimPrefix(tag, "not:"); s.HasTag(negTag) {
				return false, fmt.Sprintf("server %s has tag %s that query %s forbid", s.Name(), negTag, query.Name)
			}
			continue
		}

		// check 3 default tags: cluster, primary, standby
		switch tag {
		case "cluster":
			if s.Forked {
				return false, fmt.Sprintf("cluster level query %s will not run on forked server %v", query.Name, s.Name())
			}
			continue
		case "primary":
			if s.Recovery {
				return false, fmt.Sprintf("primary-only query %s will not run on standby server %v", query.Name, s.Name())
			}
			continue
		case "standby":
			if !s.Recovery {
				return false, fmt.Sprintf("standby-only query %s will not run on primary server %v", query.Name, s.Name())
			}
			continue
		case "pgbouncer":
			continue
		default:
			// if this tag is nether a pre-defined tag nor a prefixed pattern tag, check whether server have that tag
			if !s.HasTag(tag) {
				return false, fmt.Sprintf("server %s does not have tag %s that query %s require", s.Name(), tag, query.Name)
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
		s.queryScrapeDuration[query.Name] = query.Duration()
		if query.Error() != nil {
			s.queryScrapeErrorCount[query.Name]++
			if query.Fatal { // treat as fatal error
				log.Errorf("query %s error: %s", query.Name, query.Error())
				s.err = query.Error()
				goto final
			} else { // skip this error according to config
				log.Warnf("query %s error skipped: %s", query.Name, query.Error())
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
	failFast          bool              // fail fast instead fof waiting during start-up ?
	excludedDatabases map[string]bool   // excluded database for auto discovery
	constLabels       prometheus.Labels // prometheus const k=v labels
	tags              []string
	namespace         string

	lock    sync.RWMutex       // export lock
	server  *Server            // primary server
	servers map[string]*Server // auto discovered peripheral servers
	queries map[string]*Query  // metrics query definition

	// internal stats
	scrapeBegin time.Time // server level scrape begin
	scrapeDone  time.Time // server last scrape done

	// internal metrics: global, exporter, server, query
	up               prometheus.Gauge   // cluster level: primary target server is alive
	version          prometheus.Gauge   // cluster level: postgres main server version num
	recovery         prometheus.Gauge   // cluster level: postgres is in recovery ?
	exporterUp       prometheus.Gauge   // exporter level: always set ot 1
	exporterUptime   prometheus.Gauge   // exporter level: primary target server uptime (exporter itself)
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
	e.exporterUptime.Set(e.server.Uptime())
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
	e.exporterUptime = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: e.namespace, ConstLabels: e.constLabels,
		Subsystem: "exporter", Name: "uptime", Help: "seconds since exporter primary server inited",
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
	ch <- e.exporterUptime
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
		WithServerTags(e.tags),
	)

	// register db change callback
	if e.autoDiscovery {
		log.Infof("auto discovery is enabled, exclucded database: %v", e.excludedDatabases)
		e.server.onDatabaseChange = e.OnDatabaseChange
	}

	// check server immediately, will hang/exit according to failFast
	if err = e.server.Check(); err != nil {
		if !e.failFast {
			log.Errorf("fail connecting to primary server: %s, retrying in 10s", err.Error())
			for err != nil {
				time.Sleep(10 * time.Second)
				if err = e.server.Check(); err != nil {
					log.Errorf("fail connecting to primary server: %s, retrying in 10s", err.Error())
				}
			}
		} else {
			log.Errorf("fail connecting to primary server: %s, exit", err.Error())
		}
	}
	if err != nil {
		e.server.Plan()
	}
	e.pgbouncerMode = e.server.PgbouncerMode
	e.setupInternalMetrics()

	return
}

func (e *Exporter) OnDatabaseChange(change map[string]bool) {
	// TODO: spawn or destroy database on dbchange
	for dbname, add := range change {
		if dbname == e.server.Database {
			continue // skip primary database change
		}
		if _, found := e.excludedDatabases[dbname]; found {
			log.Infof("skip database change:%v %v according to excluded databases", dbname, add)
			continue // skip exclude databases changes
		}
		if add {
			// TODO: spawn new server
			log.Infof("database %s is installed due to auto-discovery", dbname)
		} else {
			// TODO: close old server
			log.Warnf("database %s is removed due to auto-discovery", dbname)
		}
	}
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

// WithFailFast marks exporter fail instead of waiting during start-up
func WithFailFast(failFast bool) ExporterOpt {
	return func(e *Exporter) {
		e.failFast = failFast
	}
}

// WithNamespace will specify metric namespace, by default is pg or pgbouncer
func WithNamespace(namespace string) ExporterOpt {
	return func(e *Exporter) {
		e.namespace = namespace
	}
}

// WithTags will register given tags to Exporter and all belonged servers
func WithTags(tags string) ExporterOpt {
	return func(e *Exporter) {
		e.tags = parseCSV(tags)
	}
}

// WithAutoDiscovery configures exporter with excluded database
func WithAutoDiscovery(flag bool) ExporterOpt {
	return func(e *Exporter) {
		e.autoDiscovery = flag
	}
}

// WithExcludeDatabases configures exporter with excluded database
func WithExcludeDatabases(excludeStr string) ExporterOpt {
	return func(e *Exporter) {
		exclMap := make(map[string]bool)
		exclList := parseCSV(excludeStr)
		for _, item := range exclList {
			exclMap[item] = true
		}
		e.excludedDatabases = exclMap
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

// parseCSV will turn a comma separated string into a []string
func parseCSV(s string) (tags []string) {
	s = strings.TrimSpace(s)
	if len(s) == 0 {
		return nil
	}

	parts := strings.Split(s, ",")
	for _, p := range parts {
		if tag := strings.TrimSpace(p); len(tag) > 0 {
			tags = append(tags, tag)
		}
	}

	if len(tags) == 0 {
		return nil
	}
	return
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

// parseDatname extract datname part of a dsn
func parseDatname(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return ""
	}
	return strings.TrimLeft(u.Path, "/")
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
	log.Debugf("init pg_exporter, configPath=%v constLabels=%v, disableCache=%v, autoDiscovery=%v, excludeDatabase=%v listenAdress=%v metricPath=%v",
		*configPath, *constLabels, *disableCache, *autoDiscovery, *excludeDatabase, *listenAddress, *metricPath)
	*pgURL = RetrieveTargetURL()
}

// DryRun will explain all query fetched from configs
func DryRun() {
	configs, err := LoadConfig(*configPath)
	if err != nil {
		log.Errorf("fail loading config %s, %v", *configPath, err)
		os.Exit(1)
	}

	var queries []*Query
	for _, query := range configs {
		queries = append(queries, query)
	}
	sort.Slice(queries, func(i, j int) bool {
		return queries[i].Priority < queries[j].Priority
	})
	for _, query := range queries {
		fmt.Println(query.Explain())
	}
	fmt.Println()
	os.Exit(0)

}

// Run pg exporter
func Run() {
	// explain config only
	if *dryRun {
		DryRun()
	}

	// create exporter: if target is down, exporter creation will wait until it backup online
	exporter, err := NewExporter(
		*pgURL,
		WithConfig(*configPath),
		WithConstLabels(*constLabels),
		WithCacheDisabled(*disableCache),
		WithFailFast(*failFast),
		WithNamespace(*exporterNamespace),
		WithAutoDiscovery(*autoDiscovery),
		WithExcludeDatabases(*excludeDatabase),
		WithTags(*serverTags),
	)
	if err != nil {
		log.Fatalf("fail creating pg_exporter: %w", err)
		os.Exit(2)
	}

	if *explainOnly {
		exporter.server.Plan() // trigger a manual planning before explain
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
