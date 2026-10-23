package exporter

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestClearLibPQEnvironment(t *testing.T) {
	cleared := []string{
		"PGSYSCONFDIR",
		"PGLOCALEDIR",
		"PGREALM",
		"PGSERVICEFILE",
		"PGSERVICE",
	}
	for _, name := range cleared {
		t.Setenv(name, "test-value")
	}
	t.Setenv("PGHOST", "preserved-host")

	clearLibPQEnvironment()

	for _, name := range cleared {
		if value, exists := os.LookupEnv(name); exists {
			t.Errorf("%s remains set to %q", name, value)
		}
	}
	if value := os.Getenv("PGHOST"); value != "preserved-host" {
		t.Errorf("PGHOST = %q, want %q", value, "preserved-host")
	}
}

func TestValidatedTelemetryPathReachesMetrics(t *testing.T) {
	// Detect the active mux independently of the validator and GODEBUG text.
	probe := http.NewServeMux()
	probe.Handle("/{path}", http.NotFoundHandler())
	_, pattern := probe.Handler(httptest.NewRequest(http.MethodGet, "/probe", nil))
	modernMux := pattern == "/{path}"
	for _, tt := range []struct {
		path   string
		wantOK bool
	}{
		{"/metrics", true},
		{"/custom/metrics", true},
		{"/custom/metrics/", true},
		{"/%6detrics", modernMux},
		{"/custom%2Fmetrics", modernMux},
		{"/%2e/metrics", modernMux},
		{"/^/%2f", false},
		{"/%5E/%2f", modernMux},
	} {
		t.Run(tt.path, func(t *testing.T) {
			err := validateTelemetryPath(tt.path)
			if (err == nil) != tt.wantOK {
				t.Fatalf("validateTelemetryPath(%q) error = %v, want accepted = %v (modern mux = %v)", tt.path, err, tt.wantOK, modernMux)
			}
			if err != nil {
				return
			}
			mux := http.NewServeMux()
			registerHTTPRoutes(mux, &Exporter{}, tt.path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("metrics handler"))
			}))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if w.Code != http.StatusOK || w.Body.String() != "metrics handler" {
				t.Fatalf("accepted path reaches HTTP %d, body %q", w.Code, w.Body.String())
			}
		})
	}
}

func TestValidateTelemetryPath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr string
	}{
		{name: "default", path: "/metrics"},
		{name: "custom", path: "/custom/metrics"},
		{name: "subtree", path: "/custom/metrics/"},
		{name: "redirect instead of metrics", path: "/^/%2f", wantErr: "cannot reach the metrics handler"},
		{name: "empty", path: "", wantErr: "must not be empty"},
		{name: "missing leading slash", path: "metrics", wantErr: "must start with '/'"},
		{name: "reserved endpoint", path: "/up", wantErr: "invalid or conflicting"},
		{name: "escaped reserved endpoint", path: "/%75p", wantErr: "invalid or conflicting"},
		{name: "root endpoint", path: "/", wantErr: "invalid or conflicting"},
		{name: "wildcard", path: "/{x...}", wantErr: "literal URL path"},
		{name: "end wildcard", path: "/{$}", wantErr: "literal URL path"},
		{name: "nested wildcard", path: "/metrics/{id}", wantErr: "literal URL path"},
		{name: "malformed escape", path: "/metrics/%zz", wantErr: "valid URL path"},
		{name: "query is not a path", path: "/metrics?format=text", wantErr: "valid URL path"},
		{name: "fragment is not a path", path: "/metrics#internal", wantErr: "valid URL path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTelemetryPath(tt.path)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateTelemetryPath(%q) returned unexpected error: %v", tt.path, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateTelemetryPath(%q) error = %v, want substring %q", tt.path, err, tt.wantErr)
			}
		})
	}
}
