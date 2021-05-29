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
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/log"
	"gopkg.in/yaml.v2"
	"strings"
	"sync"
	"text/template"
	"time"
)

/**********************************************************************************************\
*                                       Query                                                  *
\**********************************************************************************************/

// Query hold the information of how to fetch metric and parse them
type Query struct {
	Name   string `yaml:"name"`  // actual query name, used as metric prefix
	Desc   string `yaml:"desc"`  // description of this metric query
	SQL    string `yaml:"query"` // SQL command to fetch metrics
	Branch string `yaml:"-"`     // branch name, top layer key of config file

	// control query behaviour
	Tags       []string `yaml:"tags"`               // tags are used for execution control
	TTL        float64  `yaml:"ttl"`                // caching ttl in seconds
	Timeout    float64  `yaml:"timeout"`            // query execution timeout in seconds
	Priority   int      `yaml:"priority,omitempty"` // execution priority, from 1 to 999
	MinVersion int      `yaml:"min_version"`        // minimal supported version, include
	MaxVersion int      `yaml:"max_version"`        // maximal supported version, not include
	Fatal      bool     `yaml:"fatal"`              // if query marked fatal fail, entire scrape will fail
	Skip       bool     `yaml:"skip"`               // if query marked skip, it will be omit while loading

	Metrics []map[string]*Column `yaml:"metrics"` // metric definition list

	// metrics parsing auxiliaries
	Path        string             `yaml:"-"` // where am I from ?
	Columns     map[string]*Column `yaml:"-"` // column map
	ColumnNames []string           `yaml:"-"` // column names in origin orders
	LabelNames  []string           `yaml:"-"` // column (name) that used as label, sequences matters
	MetricNames []string           `yaml:"-"` // column (name) that used as metric
}

var queryTemplate, _ = template.New("Query").Parse(`
┏━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
┃ {{ .Name }}{{ if ne .Name .Branch}}.{{ .Branch }}{{end}}
┃ {{ .Desc }}
┣┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈
┃ Tags     ┆ {{ .Tags }}
┃ TTL      ┆ {{ .TTL }}
┃ Priority ┆ {{ .Priority }}
┃ Timeout  ┆ {{ .TimeoutDuration }}
┃ Fatal    ┆ {{ .Fatal }}
┃ Version  ┆ {{if ne .MinVersion 0}}{{ .MinVersion }}{{else}}lower{{end}} ~ {{if ne .MaxVersion 0}}{{ .MaxVersion }}{{else}}higher{{end}}
┃ Source   ┆ {{.Path }}
┣┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈
{{range .ColumnList}}┃ {{.}}
{{end}}┣┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈
{{range .MetricList}}┃ {{.}}
{{end}}┣┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈
┃ {{.TemplateSQL}}
┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
{{.MarshalYAML}}

`)

var digestTemplate, _ = template.New("Query").Parse(`

#┏━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
#┃ {{ .Name }}{{ if ne .Name .Branch}}.{{ .Branch }}{{end}}
#┃ {{ .Desc }}
#┣┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈
{{range .MetricList}}#┃ {{.}}
{{end}}#┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
{{.MarshalYAML}}

`)

// MarshalYAML will turn query into YAML format
func (q *Query) MarshalYAML() string {
	// buf := new(bytes.Buffer)
	v := make(map[string]Query, 1)
	v[q.Branch] = *q
	buf, err := yaml.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(buf)
}

// Explain will turn query into text format
func (q *Query) Explain() string {
	buf := new(bytes.Buffer)
	err := queryTemplate.Execute(buf, q)
	if err != nil {
		panic(err)
	}
	return buf.String()
}

// Digest will turn Query into a summary text format
func (q *Query) Digest() string {
	buf := new(bytes.Buffer)
	err := digestTemplate.Execute(buf, q)
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

// LabelList returns a list of label column names
func (q *Query) LabelList() []string {
	labelNames := make([]string, len(q.LabelNames))
	for i, labelName := range q.LabelNames {
		labelColumn := q.Columns[labelName]
		if labelColumn.Rename != "" {
			labelNames[i] = labelColumn.Rename
		} else {
			labelNames[i] = labelColumn.Name
		}
	}
	return labelNames
}

// MetricList returns a list of metric generated by this query
func (q *Query) MetricList() (res []string) {
	labelSignature := strings.Join(q.LabelList(), ",")
	maxSignatureLength := 0
	res = make([]string, len(q.MetricNames))

	for _, metricName := range q.MetricNames {
		metricColumnName := q.Columns[metricName].Name
		if q.Columns[metricName].Rename != "" {
			metricColumnName = q.Columns[metricName].Rename
		}
		if sigLength := len(q.Name) + len(metricColumnName) + len(labelSignature) + 3; sigLength > maxSignatureLength {
			maxSignatureLength = sigLength
		}
	}
	templateString := fmt.Sprintf("%%-%ds %%-8s %%s", maxSignatureLength+1)
	for i, metricName := range q.MetricNames {
		column := q.Columns[metricName]
		metricColumnName := q.Columns[metricName].Name
		if q.Columns[metricName].Rename != "" {
			metricColumnName = q.Columns[metricName].Rename
		}
		metricSignature := fmt.Sprintf("%s_%s{%s}", q.Name, metricColumnName, labelSignature)
		res[i] = fmt.Sprintf(templateString, metricSignature, column.Usage, column.Desc)
	}

	return
}

// TemplateSQL will format SQL string with padding
func (q *Query) TemplateSQL() string {
	return strings.Replace(q.SQL, "\n", "\n┃ ", -1)
}

// TimeoutDuration will turn timeout settings into time.Duration
func (q *Query) TimeoutDuration() time.Duration {
	return time.Duration(float64(time.Second) * q.Timeout)
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
	lock        sync.RWMutex                // scrape lock
	result      []prometheus.Metric         // cached metrics
	descriptors map[string]*prometheus.Desc // maps column index to descriptor, build on init
	cacheHit    bool                        // indicate last scrape was served from cache or real execution
	err         error

	// stats
	lastScrape     time.Time     // SERVER's scrape start time (for cache window align)
	scrapeBegin    time.Time     // execution begin time
	scrapeDone     time.Time     // execution complete time
	scrapeDuration time.Duration // last real execution duration
}

// NewQueryInstance will generate query instance from query, Injecting a server object
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
	q.scrapeBegin = time.Now()
	if q.cacheExpired() || q.Server.DisableCache {
		q.execute()
		q.cacheHit = false
		q.scrapeDone = time.Now()
		q.scrapeDuration = q.scrapeDone.Sub(q.scrapeBegin)
		q.lastScrape = q.Server.scrapeBegin
	} else { // serve from cache
		q.cacheHit = true
		q.scrapeDone = time.Now()
	}
	q.sendMetrics(ch) // the cache is already reset to zero even execute failed
}

// ResultSize report last scrapped metric count
func (q *QueryInstance) ResultSize() int {
	return len(q.result)
}

// Error wraps query error
func (q *QueryInstance) Error() error {
	return q.err
}

// Duration returns last scrape duration in float64 seconds
func (q *QueryInstance) Duration() float64 {
	return q.scrapeDone.Sub(q.scrapeBegin).Seconds()
}

// CacheHit report whether last scrape was serve from cache
func (q *QueryInstance) CacheHit() bool {
	return q.cacheHit
}

// execute will run this query to registered server, result and err are registered
func (q *QueryInstance) execute() {
	q.result = q.result[:0] // reset cache
	var rows *sql.Rows
	var err error

	// execution
	if q.Timeout != 0 { // if timeout is provided, use context
		log.Debugf("query [%s] @ server [%s] executing begin with time limit: %v", q.Name, q.Server.Database, q.TimeoutDuration())
		ctx, cancel := context.WithTimeout(context.Background(), q.TimeoutDuration())
		defer cancel()
		rows, err = q.Server.QueryContext(ctx, q.SQL)
	} else {
		log.Debugf("query [%s] executing begin", q.Name)
		rows, err = q.Server.Query(q.SQL)
	}

	// error handling: if query failed because of timeout or error, record and return
	if err != nil {
		if err == context.DeadlineExceeded { // timeout
			q.err = fmt.Errorf("query [%s] timeout because duration %v exceed limit %v",
				q.Name, time.Now().Sub(q.scrapeBegin), q.TimeoutDuration())
		} else {
			q.err = fmt.Errorf("query [%s] failed: %w", q.Name, err)
		}
		return
	}
	defer rows.Close()

	// parsing meta:  fetch column metadata for dynamic name lookup
	columnNames, err := rows.Columns()
	if err != nil {
		q.err = fmt.Errorf("query [%s] fail retriving rows meta: %w", q.Name, err)
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
	if len(columnNames) != len(q.Columns) { // warn if column count not match
		log.Warnf("query [%s] column count not match, result %d ≠ config %d", q.Name, len(columnNames), len(q.Columns))
	}

	// scan loop: for each row, extract labels from all label columns, then generate a new metric for each metric column
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
	log.Debugf("query [%s] executing complete in %v, metrics count: %d",
		q.Name, time.Now().Sub(q.scrapeBegin), len(q.result))
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
