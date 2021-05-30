package exporter

import (
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
	"strings"
)

/**********************************************************************************************\
*                                       Column                                                 *
\**********************************************************************************************/
const (
	DISCARD   = "DISCARD"   // Ignore this column (when SELECT *)
	LABEL     = "LABEL"     // Use this column as a label
	COUNTER   = "COUNTER"   // Use this column as a counter
	GAUGE     = "GAUGE"     // Use this column as a gauge
	HISTOGRAM = "HISTOGRAM" // Use this column as a histogram
)

// ColumnUsage determine how to use query result column
var ColumnUsage = map[string]bool{
	DISCARD:   false,
	LABEL:     false,
	COUNTER:   true,
	GAUGE:     true,
	HISTOGRAM: true,
}

// Column holds the metadata of query result
type Column struct {
	Name    string    `yaml:"name"`
	Usage   string    `yaml:"usage,omitempty"`   // column usage
	Rename  string    `yaml:"rename,omitempty"`  // rename column
	Bucket  []float64 `yaml:"bucket,omitempty"`  // histogram bucket
	Scale   float64   `yaml:"scale,omitempty"`   // scale factor
	Default string    `yaml:"default,omitempty"` // default value
	Desc    string    `yaml:"description,omitempty"`
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
