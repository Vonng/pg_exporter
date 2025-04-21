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
	Branch    = "main"
	Revision  = "HEAD"
	BuildDate = "20250421212100"
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
