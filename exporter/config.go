package exporter

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"os"
	"path"
	"strings"
)

// GetConfig will try load config from target path
func GetConfig() (res string) {
	// priority: cli-args > env  > default settings (check exist)
	if res = *configPath; res != "" {
		logInfof("retrieve config path %s from command line", res)
		return res
	}
	if res = os.Getenv("PG_EXPORTER_CONFIG"); res != "" {
		logInfof("retrieve config path %s from PG_EXPORTER_CONFIG", res)
		return res
	}

	candidate := []string{"pg_exporter.yml", "/etc/pg_exporter.yml", "/etc/pg_exporter"}
	for _, res = range candidate {
		if _, err := os.Stat(res); err == nil { // default1 exist
			logInfof("fallback on default config path: %s", res)
			return res
		}
	}
	return ""
}

// ParseConfig turn config content into Query struct
func ParseConfig(content []byte) (queries map[string]*Query, err error) {
	queries = make(map[string]*Query)
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
		files, err := os.ReadDir(configPath)
		if err != nil {
			return nil, fmt.Errorf("fail reading config dir: %s: %w", configPath, err)
		}

		logDebugf("load config from dir: %s", configPath)
		confFiles := make([]string, 0)
		for _, conf := range files {
			if !(strings.HasSuffix(conf.Name(), ".yaml") || strings.HasSuffix(conf.Name(), ".yml")) && !conf.IsDir() { // depth = 1
				continue // skip non yaml files
			}
			confFiles = append(confFiles, path.Join(configPath, conf.Name()))
		}

		// make global config map and assign priority according to config file alphabetic orders
		// priority is an integer range from 1 to 999, where 1 - 99 is reserved for user
		queries = make(map[string]*Query)
		var queryCount, configCount int
		for _, confPath := range confFiles {
			if singleQueries, err := LoadConfig(confPath); err != nil {
				logWarnf("skip config %s due to error: %s", confPath, err.Error())
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
		logDebugf("load %d of %d queries from %d config files", len(queries), queryCount, configCount)
		return queries, nil
	}

	// single file case: recursive exit condition
	content, err := os.ReadFile(configPath)
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
		// if timeout is set to a neg number, set to 0, so it's actually disabled
		if q.Timeout == 0 {
			q.Timeout = 0.1
		}
		if q.Timeout < 0 {
			q.Timeout = 0
		}
	}
	logDebugf("load %d queries from %s, ", len(queries), configPath)
	return queries, nil

}
