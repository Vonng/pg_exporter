package exporter

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
)

/* ================ Logger ================ */

func configureLogger(levelStr, formatStr string) log.Logger {
	var logger log.Logger

	switch formatStr {
	case "json":
		logger = log.NewJSONLogger(log.NewSyncWriter(os.Stderr))
	case "logfmt", "":
		logger = log.NewLogfmtLogger(log.NewSyncWriter(os.Stderr))
	default:
		panic("unknown log format: " + formatStr)
	}

	var lvl level.Option
	switch strings.ToLower(levelStr) {
	case "debug":
		lvl = level.AllowDebug()
	case "info":
		lvl = level.AllowInfo()
	case "warn":
		lvl = level.AllowWarn()
	case "error":
		lvl = level.AllowError()
	default:
		lvl = level.AllowInfo() // fallback to default info level
	}

	logger = level.NewFilter(logger, lvl)
	logger = log.With(logger, "timestamp", log.DefaultTimestampUTC, "caller", log.Caller(4))
	return logger
}

// logDebugf will log debug message
func logDebugf(format string, v ...interface{}) {
	_ = level.Debug(Logger).Log("msg", fmt.Sprintf(format, v...))
}

// logInfof will log info message
func logInfof(format string, v ...interface{}) {
	_ = level.Info(Logger).Log("msg", fmt.Sprintf(format, v...))
}

// logWarnf will log warning message
func logWarnf(format string, v ...interface{}) {
	_ = level.Warn(Logger).Log("msg", fmt.Sprintf(format, v...))
}

// logErrorf will log error message
func logErrorf(format string, v ...interface{}) {
	_ = level.Error(Logger).Log("msg", fmt.Sprintf(format, v...))
}

// logError will print error message directly
func logError(msg string) {
	_ = level.Error(Logger).Log("msg", msg)
}

// logFatalf will log error message
func logFatalf(format string, v ...interface{}) {
	_ = level.Error(Logger).Log("msg", fmt.Sprintf(format, v...))
}

/* ================ Auxiliaries ================ */

// castFloat64 will cast datum into float64 with scale & default value
func castFloat64(t interface{}, s string, d string) float64 {
	var scale = 1.0
	if s != "" {
		if scaleFactor, err := strconv.ParseFloat(s, 64); err != nil {
			logWarnf("invalid column scale: %v ", s)
		} else {
			scale = scaleFactor
		}
	}

	switch v := t.(type) {
	case int64:
		return float64(v) * scale
	case float64:
		return v * scale
	case time.Time:
		return float64(v.Unix())
	case []byte:
		strV := string(v)
		result, err := strconv.ParseFloat(strV, 64)
		if err != nil {
			logWarnf("fail casting []byte to float64: %v", t)
			return math.NaN()
		}
		return result * scale
	case string:
		result, err := strconv.ParseFloat(v, 64)
		if err != nil {
			logWarnf("fail casting string to float64: %v", t)
			return math.NaN()
		}
		return result * scale
	case bool:
		if v {
			return 1.0
		}
		return 0.0
	case nil:
		if d != "" {
			result, err := strconv.ParseFloat(d, 64)
			if err != nil {
				logWarnf("invalid column default: %v", d)
				return math.NaN()
			}
			return result
		}
		return math.NaN()
	default:
		logWarnf("fail casting unknown to float64: %v", t)
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
		logWarnf("fail casting unknown to string: %v", t)
		return ""
	}
}

// parseConstLabels turn param string into prometheus.Labels
func parseConstLabels(s string) prometheus.Labels {
	labels := make(prometheus.Labels)
	s = strings.TrimSpace(s)
	if len(s) == 0 {
		return nil
	}

	parts := strings.Split(s, ",")
	for _, p := range parts {
		keyValue := strings.Split(strings.TrimSpace(p), "=")
		if len(keyValue) != 2 {
			logErrorf(`malformed labels format %q, should be "key=value"`, p)
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
