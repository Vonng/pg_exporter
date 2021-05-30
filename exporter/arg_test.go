package exporter

import "testing"

func TestProcessPostgresDSN(t *testing.T) {
	ProcessPostgresDSN("postgres:///")
	ProcessPostgresDSN("postgres:///?sslmode=disabled")
	ProcessPostgresDSN("postgres://10.10.10.10:5432/")
	ProcessPostgresDSN("postgres://10.10.10.10:5432/postgres")
	ProcessPostgresDSN("dbname=meta user=dbuser_meta host=/tmp")
}