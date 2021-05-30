package exporter

import (
	"bytes"
	"fmt"
	"gopkg.in/yaml.v3"
	"strings"
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
