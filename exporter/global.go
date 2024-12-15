package exporter

import (
	"sync"

	"github.com/go-kit/kit/log"
)

/* ================ Parameters ================ */

// Version is read by make build procedure
var Version = "0.7.1"

var defaultPGURL = "postgresql:///?sslmode=disable"

/* ================ Global Vars ================ */

// PgExporter is the global singleton of Exporter
var (
	PgExporter *Exporter
	ReloadLock sync.Mutex
	Logger     log.Logger
)
