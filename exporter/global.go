package exporter

import (
	"sync"
)

/**********************************************************************************************\
*                                       Parameters                                             *
\**********************************************************************************************/

// Version is read by make build procedure
var Version = "0.6.0"

var defaultPGURL = "postgresql:///?sslmode=disable"

/**********************************************************************************************\
 *                                        Globals                                               *
 \**********************************************************************************************/
// PgExporter is the global singleton of Exporter
var (
	PgExporter *Exporter
	ReloadLock sync.Mutex
)
