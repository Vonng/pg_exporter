package exporter

import (
	"log/slog"
	"sync"
)

/* ================ Parameters ================ */

// Version is read by make build procedure
var Version = "0.8.0"

var defaultPGURL = "postgresql:///?sslmode=disable"

/* ================ Global Vars ================ */

// PgExporter is the global singleton of Exporter
var (
	PgExporter *Exporter
	ReloadLock sync.Mutex
	Logger     *slog.Logger
)
