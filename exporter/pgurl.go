package exporter

import (
	"github.com/prometheus/common/log"
	"io/ioutil"
	"net/url"
	"os"
	"strings"
)

// GetPGURL will retrive, parse, modify postgres connection string
func GetPGURL() string {
	return ProcessPGURL(RetrievePGURL())
}

// RetrievePGURL retrieve pg target url from multiple sources according to precedence
// priority: cli-args > env  > env file path
//    1. Command Line Argument (--url -u -d)
//    2. Environment PG_EXPORTER_URL
//    3. From file specified via Environment PG_EXPORTER_URL_FILE
//    4. Default url
func RetrievePGURL() (res string) {
	// command line args
	if *pgURL != "" {
		log.Infof("retrieve target url %s from command line", ShadowPGURL(*pgURL))
		return *pgURL
	}
	// env PG_EXPORTER_URL
	if res = os.Getenv("PG_EXPORTER_URL"); res != "" {
		log.Infof("retrieve target url %s from PG_EXPORTER_URL", ShadowPGURL(*pgURL))
		return res
	}
	// env PGURL
	if res = os.Getenv("PGURL"); res != "" {
		log.Infof("retrieve target url %s from PGURL", ShadowPGURL(*pgURL))
		return res
	}
	// file content from file PG_EXPORTER_URL_FILE
	if filename := os.Getenv("PG_EXPORTER_URL_FILE"); filename != "" {
		if fileContents, err := ioutil.ReadFile(filename); err != nil {
			log.Fatalf("PG_EXPORTER_URL_FILE=%s is specified, fail loading url, exit", err.Error())
			os.Exit(-1)
		} else {
			res = strings.TrimSpace(string(fileContents))
			log.Infof("retrieve target url %s from PG_EXPORTER_URL_FILE", ShadowPGURL(res))
			return res
		}
	}
	// DEFAULT
	log.Warnf("fail retrieving target url, fallback on default url: %s", defaultPGURL)
	return defaultPGURL
}

// ProcessPGURL will fix URL with default options
func ProcessPGURL(pgurl string) string {
	u, err := url.Parse(pgurl)
	if err != nil {
		log.Errorf("invalid url format %s", pgurl)
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

// ShadowPGURL will hide password part of dsn
func ShadowPGURL(pgurl string) string {
	parsedURL, err := url.Parse(pgurl)
	// That means we got a bad connection string. Fail early
	if err != nil {
		log.Fatalf("Could not parse connection string %s", err.Error())
		os.Exit(-1)
	}
	// We need to handle two cases:
	// 1. The password is in the format postgresql://localhost:5432/postgres?sslmode=disable&user=<user>&password=<pass>
	// 2. The password is in the format postgresql://<user>:<pass>@localhost:5432/postgres?sslmode=disable

	qs := parsedURL.Query()
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
	parsedURL.RawQuery = buf.String()
	return parsedURL.Redacted()
}

// ParseDatname extract database name part of a pgurl
func ParseDatname(pgurl string) string {
	u, err := url.Parse(pgurl)
	if err != nil {
		return ""
	}
	return strings.TrimLeft(u.Path, "/")
}

// ReplaceDatname will replace pgurl with new database name
func ReplaceDatname(pgurl, datname string) string {
	u, err := url.Parse(pgurl)
	if err != nil {
		log.Errorf("invalid url format %s", pgurl)
		return ""
	}
	u.Path = "/" + datname
	return u.String()
}
