package exporter

import (
	"bytes"
	"log/slog"
	"net/url"
	"strings"
	"testing"

	"github.com/lib/pq"
)

func TestDatabaseURLMatchesDriver(t *testing.T) {
	for _, input := range []string{
		"postgres://monitor@localhost/aaa?dbname=zzz",
		"postgres://monitor@localhost/zzz?dbname=aaa",
		"postgres://monitor@localhost/postgres?database=pgbouncer",
		"postgres://monitor@localhost/?dbname=%20db%20",
		"postgres://monitor@localhost/%2Fdb",
	} {
		t.Run(input, func(t *testing.T) {
			cfg, err := pq.NewConfig(input)
			if err != nil {
				t.Fatal(err)
			}
			if got := ParseDatname(input); got != cfg.Database {
				t.Errorf("ParseDatname = %q; driver database = %q", got, cfg.Database)
			}
			for _, name := range []string{"aaa", "zzz", " space /?#%数据库 "} {
				changed := ReplaceDatname(input, name)
				cfg, err := pq.NewConfig(changed)
				if err != nil {
					t.Fatal(err)
				}
				if cfg.Database != name {
					t.Errorf("ReplaceDatname(%q) connects to %q", name, cfg.Database)
				}
			}
		})
	}
}

func TestProcessPGURLRejectsMalformedInput(t *testing.T) {
	var logs bytes.Buffer
	originalLogger := Logger
	Logger = slog.New(slog.NewTextHandler(&logs, nil))
	t.Cleanup(func() { Logger = originalLogger })
	for _, input := range []string{
		"postgres://monitor:secret@localhost:bad/postgres",
		"postgres://monitor:secret@localhost/db?sslmode=verify-full%zz",
		"postgres://monitor:secret@localhost/db?password=secret;sslmode=verify-full",
		"postgres://monitor:secret@localhost/db#fragment",
		"postgres://monitor@localhost/db?password%20=secret",
		"postgres://monitor@localhost/db?sslmode%20=verify-full",
		"postgres://monitor@localhost/db?database%20=locked",
		"postgres://monitor@localhost/db?password%3Dsecret=rest",
		"postgres://monitor@localhost/db?password%E2%80%83=secret",
		"postgres://monitor@localhost/db?=secret",
		"https://monitor:secret@localhost/db", "", "dbname=postgres password=secret",
	} {
		t.Run(input, func(t *testing.T) {
			logs.Reset()
			if got := ProcessPGURL(input); got != "" {
				t.Errorf("malformed input was accepted: %q", got)
			}
			if got := ReplaceDatname(input, "app"); got != "" {
				t.Errorf("malformed input was accepted during discovery: %q", got)
			}
			if got := ShadowPGURL(input); got != "<invalid PostgreSQL URL>" {
				t.Errorf("malformed input was exposed in redacted URL: %q", got)
			}
			if strings.Contains(logs.String(), "secret") {
				t.Errorf("credentials leaked in error log: %q", logs.String())
			}
		})
	}
}

func TestProcessPGURLPreservesValidQueryParameters(t *testing.T) {
	input := "postgres://monitor@localhost/db?%70assword=p%20q%3Dx&sslmode=verify-full&my_extension.setting=some%20value"
	processed := ProcessPGURL(input)
	if processed == "" {
		t.Fatal("valid URL was rejected")
	}
	cfg, err := pq.NewConfig(processed)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Password != "p q=x" || cfg.Database != "db" || cfg.SSLMode != "verify-full" || cfg.Runtime["my_extension.setting"] != "some value" {
		t.Fatal("valid query parameters changed during normalization")
	}
	redacted, err := url.Parse(ShadowPGURL(processed))
	if err != nil || redacted.Query().Get("password") != "xxxxx" {
		t.Fatal("encoded password parameter was not redacted")
	}
}

func TestShadowPGURLInvalidInputDoesNotLeak(t *testing.T) {
	for _, input := range []string{
		"postgres://monitor:secret@localhost:bad/db",
		"postgres://monitor:secret@localhost/db?password=secret%zz",
		"postgres://monitor:secret@localhost/db#secret",
		"dbname=postgres password=secret",
	} {
		if got := ShadowPGURL(input); strings.Contains(got, "secret") || got == input {
			t.Errorf("unsafe redaction: %q", got)
		}
	}
}
