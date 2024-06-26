package exporter

import (
	"github.com/go-kit/kit/log"
	"sync"
)

/* ================ Parameters ================ */

// Version is read by make build procedure
var Version = "0.7.0"

var defaultPGURL = "postgresql:///?sslmode=disable"

/* ================ Global Vars ================ */

// PgExporter is the global singleton of Exporter
var (
	PgExporter *Exporter
	ReloadLock sync.Mutex
	Logger     log.Logger
)
