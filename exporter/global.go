package exporter

import (
	"log/slog"
	"runtime"
	"sync"
)

/* ================ Parameters ================ */

// Version is read by make build procedure
var Version = "0.9.0"

// Build information. Populated at build-time.
var (
	Branch    = "HEAD"
	Revision  = "unknown"
	BuildDate = "unknown"
	GoVersion = runtime.Version()
	GOOS      = runtime.GOOS
	GOARCH    = runtime.GOARCH
)

var defaultPGURL = "postgresql:///?sslmode=disable"

/* ================ Global Vars ================ */

// PgExporter is the global singleton of Exporter
var (
	PgExporter *Exporter
	ReloadLock sync.Mutex
	Logger     *slog.Logger
)
