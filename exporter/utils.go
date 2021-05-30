package exporter

import (
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/log"
	"math"
	"strconv"
	"strings"
	"time"
)

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
