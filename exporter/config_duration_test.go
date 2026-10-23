package exporter

import (
	"fmt"
	"strings"
	"testing"
)

func TestConfigRejectsInvalidDurations(t *testing.T) {
	for _, value := range []string{".nan", ".inf", "-.inf", "1e20", "9223372036.854776"} {
		for _, field := range []string{"ttl", "timeout", "predicate"} {
			t.Run(field+"/"+value, func(t *testing.T) {
				setting := fmt.Sprintf("  %s: %s\n", field, value)
				if field == "predicate" {
					setting = fmt.Sprintf("  predicate_queries:\n    - predicate_query: SELECT true\n      ttl: %s\n", value)
				}
				config := "q:\n  query: SELECT 1 AS value\n" + setting + "  metrics:\n    - value: {usage: GAUGE}\n"
				if _, err := ParseQuery(config); err == nil {
					t.Fatal("invalid duration accepted")
				} else if !strings.Contains(err.Error(), "ttl") && !strings.Contains(err.Error(), "timeout") {
					t.Fatalf("rejected for a reason unrelated to durations: %v", err)
				}
			})
		}
	}
}

func TestConfigKeepsTimeoutAndCacheDefaults(t *testing.T) {
	for _, timeout := range []float64{0, -1, -1e20, 0.25} {
		config := fmt.Sprintf("q:\n  query: SELECT 1 AS value\n  ttl: 0\n  timeout: %g\n  predicate_queries:\n    - predicate_query: SELECT true\n      ttl: 60\n  metrics:\n    - value: {usage: GAUGE}\n", timeout)
		q, err := ParseQuery(config)
		if err != nil {
			t.Fatal(err)
		}
		want := timeout
		if timeout == 0 {
			want = 0.1
		}
		if timeout < 0 {
			want = 0
		}
		if q.Timeout != want || q.TTL != 0 {
			t.Fatalf("defaults changed: %#v", q)
		}
	}
}
