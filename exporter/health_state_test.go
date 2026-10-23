package exporter

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"

	"github.com/lib/pq"
)

func TestIsPostgresStartupError(t *testing.T) {
	if !isPostgresStartupError(&pq.Error{Code: pgSQLStateCannotConnectNow}) {
		t.Fatal("expected SQLSTATE 57P03 to be recognized as startup error")
	}

	wrapped := fmt.Errorf("wrapped: %w", &pq.Error{Code: pgSQLStateCannotConnectNow})
	if !isPostgresStartupError(wrapped) {
		t.Fatal("expected wrapped SQLSTATE 57P03 to be recognized as startup error")
	}

	if isPostgresStartupError(errors.New("plain error")) {
		t.Fatal("plain error should not be recognized as startup error")
	}
	if isPostgresStartupError(&pq.Error{Code: "08006"}) {
		t.Fatal("non-57P03 postgres error should not be recognized as startup error")
	}
}

func TestHealthResponseUsesOneSnapshot(t *testing.T) {
	original := PgExporter
	t.Cleanup(func() { setCurrentExporter(original) })
	e := &Exporter{}
	setCurrentExporter(e)
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			e.updateHealthState(true, true)
			e.updateHealthState(false, false)
			e.updateHealthState(true, false)
			e.updateHealthStateWithStartup(false, false, true)
			runtime.Gosched()
		}
	}()
	defer func() { close(done); wg.Wait() }()
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"up", e.UpCheckFunc}, {"primary", e.PrimaryCheckFunc}, {"replica", e.ReplicaCheckFunc},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/"+tc.name, nil)
			for range 20000 {
				w := httptest.NewRecorder()
				tc.handler(w, req)
				status := w.Body.String()
				want := http.StatusServiceUnavailable
				if status == "primary" || status == "replica" {
					want = http.StatusOK
					if tc.name != "up" && tc.name != status {
						want = http.StatusNotFound
					}
				}
				if w.Code != want {
					t.Fatalf("HTTP %d with body %q; want %d", w.Code, status, want)
				}
			}
		})
	}
}

func TestUpdateHealthStateWithStartup(t *testing.T) {
	e := &Exporter{}

	e.updateHealthStateWithStartup(false, false, true)
	if e.Up() {
		t.Fatal("startup state should not be considered up")
	}
	if e.Recovery() {
		t.Fatal("startup state should not expose recovery=true")
	}
	if got := e.Status(); got != "starting" {
		t.Fatalf("status = %s, want starting", got)
	}

	e.updateHealthStateWithStartup(false, false, false)
	if got := e.Status(); got != "down" {
		t.Fatalf("status = %s, want down", got)
	}

	e.updateHealthStateWithStartup(true, true, false)
	if got := e.Status(); got != "replica" {
		t.Fatalf("status = %s, want replica", got)
	}

	e.updateHealthStateWithStartup(true, false, false)
	if got := e.Status(); got != "primary" {
		t.Fatalf("status = %s, want primary", got)
	}
}
