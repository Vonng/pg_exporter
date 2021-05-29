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
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/log"
	"gopkg.in/alecthomas/kingpin.v2"
	"io/ioutil"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

/**********************************************************************************************\
*                                       Parameters                                             *
\**********************************************************************************************/

// Version is read by make build procedure
var Version = "0.4.0"

var defaultPGURL = "postgresql:///?sslmode=disable"

var (
	// exporter settings
	pgURL             = kingpin.Flag("url", "postgres target url").String()
	configPath        = kingpin.Flag("config", "path to config dir or file").String()
	constLabels       = kingpin.Flag("label", "constant lables:comma separated list of label=value pair").Default("").Envar("PG_EXPORTER_LABEL").String()
	serverTags        = kingpin.Flag("tag", "tags,comma separated list of server tag").Default("").Envar("PG_EXPORTER_TAG").String()
	disableCache      = kingpin.Flag("disable-cache", "force not using cache").Default("false").Envar("PG_EXPORTER_DISABLE_CACHE").Bool()
	disableIntro      = kingpin.Flag("disable-intro", "disable collector level introspection metrics").Default("false").Envar("PG_EXPORTER_DISABLE_INTRO").Bool()
	autoDiscovery     = kingpin.Flag("auto-discovery", "automatically scrape all database for given server").Default("false").Envar("PG_EXPORTER_AUTO_DISCOVERY").Bool()
	excludeDatabase   = kingpin.Flag("exclude-database", "excluded databases when enabling auto-discovery").Default("template0,template1,postgres").Envar("PG_EXPORTER_EXCLUDE_DATABASE").String()
	includeDatabase   = kingpin.Flag("include-database", "included databases when enabling auto-discovery").Default("").Envar("PG_EXPORTER_INCLUDE_DATABASE").String()
	exporterNamespace = kingpin.Flag("namespace", "prefix of built-in metrics, (pg|pgbouncer) by default").Default("").Envar("PG_EXPORTER_NAMESPACE").String()
	failFast          = kingpin.Flag("fail-fast", "fail fast instead of waiting during start-up").Envar("PG_EXPORTER_FAIL_FAST").Default("false").Bool()

	// prometheus http
	listenAddress = kingpin.Flag("web.listen-address", "prometheus web server listen address").Default(":9630").Envar("PG_EXPORTER_LISTEN_ADDRESS").String()
	metricPath    = kingpin.Flag("web.telemetry-path", "URL path under which to expose metrics.").Default("/metrics").Envar("PG_EXPORTER_TELEMETRY_PATH").String()

	// action
	dryRun      = kingpin.Flag("dry-run", "dry run and print raw configs").Default("false").Bool()
	explainOnly = kingpin.Flag("explain", "explain server planned queries").Default("false").Bool()
)

/**********************************************************************************************\
 *                                        Globals                                               *
 \**********************************************************************************************/
// PgExporter is the global singleton of Exporter
var (
	PgExporter *Exporter
	ReloadLock sync.Mutex
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

// shadowDSN will hide password part of dsn
func shadowDSN(dsn string) string {
	pDSN, err := url.Parse(dsn)
	// That means we got a bad connection string. Fail early
	if err != nil {
		log.Fatalf("Could not parse the connection string", err)
		os.Exit(-1)
	}
	// We need to handle two cases:
	// 1. The password is in the format postgresql://localhost:5432/postgres?sslmode=disable&user=<user>&password=<pass>
	// 2. The password is in the format postgresql://<user>:<pass>@localhost:5432/postgres?sslmode=disable

	qs := pDSN.Query()
	var buf strings.Builder
	for k, v := range qs {
		if len(v) == 0 {
			continue
		}
		if buf.Len() > 0 {
			buf.WriteByte('&')
		}
		buf.WriteString(k)
		buf.WriteByte('=')
		if strings.ToLower(k) == "password" {
			buf.WriteString("xxxxx")
		} else {
			buf.WriteString(v[0])
		}
	}
	pDSN.RawQuery = buf.String()
	return pDSN.Redacted()
}

// parseDatname extract datname part of a dsn
func parseDatname(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return ""
	}
	return strings.TrimLeft(u.Path, "/")
}

// RetrieveTargetURL retrieve pg target url from multiple sources according to precedence
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
	if res = os.Getenv("DATA_SOURCE_NAME"); res != "" {
		log.Infof("retrieve target url %s from DATA_SOURCE_NAME", shadowDSN(*pgURL))
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

	// process URL (add missing sslmode)
	return defaultPGURL
}

// ProcessURL will fix URL with default options
func ProcessURL(pgUrlStr string) string {
	u, err := url.Parse(pgUrlStr)
	if err != nil {
		log.Errorf("invalid url format %s", pgUrlStr)
		return ""
	}

	// add sslmode = disable if not exists
	qs := u.Query()
	if sslmode := qs.Get(`sslmode`); sslmode == "" {
		qs.Set(`sslmode`, `disable`)
	}
	var buf strings.Builder
	for k, v := range qs {
		if len(v) == 0 {
			continue
		}
		if buf.Len() > 0 {
			buf.WriteByte('&')
		}
		buf.WriteString(k)
		buf.WriteByte('=')
		buf.WriteString(v[0])
	}
	u.RawQuery = buf.String()
	return u.String()
}

// replace
func replaceUrlDatabase(pgUrlStr, dbname string) string {
	u, err := url.Parse(pgUrlStr)
	if err != nil {
		log.Errorf("invalid url format %s", pgUrlStr)
		return ""
	}
	u.Path = "/" + dbname
	return u.String()
}

// RetrieveConfig config path
func RetrieveConfig() (res string) {
	// priority: cli-args > env  > default settings (check exist)
	if res = *configPath; res != "" {
		log.Infof("retrieve config path %s from command line", res)
		return res
	}
	if res = os.Getenv("PG_EXPORTER_CONFIG"); res != "" {
		log.Infof("retrieve config path %s from PG_EXPORTER_CONFIG", res)
		return res
	}

	candidate := []string{"pg_exporter.yaml", "pg_exporter.yml", "/etc/pg_exporter.yaml", "/etc/pg_exporter"}
	for _, res = range candidate {
		if _, err := os.Stat(res); err == nil { // default1 exist
			log.Infof("fallback on default config path: %s", res)
			return res
		}
	}
	return ""
}
