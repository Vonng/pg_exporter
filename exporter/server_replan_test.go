package exporter

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// Script the metadata replies while using the real precheck and planning path.
type metadataTestConnector struct{ conn *metadataTestConn }

func (c metadataTestConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c metadataTestConnector) Driver() driver.Driver                        { return histogramTestDriver{} }

type metadataTestConn struct {
	histogramTestConn
	username, namespaces, extensions string
}

func (c *metadataTestConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(0), nil
}
func (c *metadataTestConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if strings.HasPrefix(query, "SHOW server_version_num") {
		return &histogramTestRows{columns: []string{"version"}, values: [][]driver.Value{{int64(180006)}}}, nil
	}
	if strings.HasPrefix(query, "SELECT current_catalog") {
		return &histogramTestRows{
			columns: []string{"database", "username", "recovery", "databases", "namespaces", "extensions"},
			values:  [][]driver.Value{{"postgres", c.username, false, "{postgres}", c.namespaces, c.extensions}},
		}, nil
	}
	return nil, fmt.Errorf("unexpected query: %s", query)
}

func TestPrecheckReplansOnEligibilityChanges(t *testing.T) {
	conn := &metadataTestConn{username: "monitor", namespaces: "{public}", extensions: "{plpgsql}"}
	db := sql.OpenDB(metadataTestConnector{conn: conn})
	t.Cleanup(func() { _ = db.Close() })
	s := NewServer("postgres://monitor@localhost/postgres", WithQueries(map[string]*Query{
		"schema":    makeGaugeQuery("schema", 1, "schema:custom"),
		"extension": makeGaugeQuery("extension", 2, "extension:hstore"),
		"user":      makeGaugeQuery("user", 3, "username:renamed"),
	}))
	s.DB = db
	for _, step := range []struct {
		name, user, namespaces, extensions string
		want                               []string
		changed                            bool
	}{
		{"initial", "monitor", "{public}", "{plpgsql}", nil, true},
		{"add schema", "monitor", "{public,custom}", "{plpgsql}", []string{"schema"}, true},
		{"add extension", "monitor", "{public,custom}", "{plpgsql,hstore}", []string{"extension", "schema"}, true},
		{"same sets reordered", "monitor", "{custom,public}", "{hstore,plpgsql}", []string{"extension", "schema"}, false},
		{"rename user", "renamed", "{public,custom}", "{plpgsql,hstore}", []string{"extension", "schema", "user"}, true},
		{"drop schema and extension", "renamed", "{public}", "{plpgsql}", []string{"user"}, true},
	} {
		t.Run(step.name, func(t *testing.T) {
			conn.username, conn.namespaces, conn.extensions = step.user, step.namespaces, step.extensions
			if err := s.Check(); err != nil {
				t.Fatal(err)
			}
			if s.Planned == step.changed {
				t.Errorf("planned=%v after metadata change=%v", s.Planned, step.changed)
			}
			if !s.Planned {
				s.Plan()
			}
			var got []string
			for _, c := range s.Collectors {
				got = append(got, c.Name)
			}
			sort.Strings(got)
			if !reflect.DeepEqual(got, step.want) {
				t.Errorf("selected collectors %v; want %v", got, step.want)
			}
		})
	}
}
