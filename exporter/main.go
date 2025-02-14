package exporter

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"syscall"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/exporter-toolkit/web"
)

// DryRun will explain all query fetched from configs
func DryRun() {
	configs, err := LoadConfig(*configPath)
	if err != nil {
		logErrorf("fail loading config %s, %v", *configPath, err)
		os.Exit(1)
	}

	var queries []*Query
	for _, query := range configs {
		queries = append(queries, query)
	}
	sort.Slice(queries, func(i, j int) bool {
		return queries[i].Priority < queries[j].Priority
	})
	for _, query := range queries {
		fmt.Println(query.Explain())
	}
	fmt.Println()
	os.Exit(0)

}

// Reload will launch a new pg exporter instance
func Reload() error {
	ReloadLock.Lock()
	defer ReloadLock.Unlock()
	logDebugf("reload request received, launch new exporter instance")

	// create a new exporter
	newExporter, err := NewExporter(
		*pgURL,
		WithConfig(*configPath),
		WithConstLabels(*constLabels),
		WithCacheDisabled(*disableCache),
		WithIntroDisabled(*disableIntro),
		WithFailFast(*failFast),
		WithNamespace(*exporterNamespace),
		WithAutoDiscovery(*autoDiscovery),
		WithExcludeDatabase(*excludeDatabase),
		WithIncludeDatabase(*includeDatabase),
		WithTags(*serverTags),
		WithConnectTimeout(*connectTimeout),
	)
	// if launch new exporter failed, do nothing
	if err != nil {
		logErrorf("fail to reload exporter: %s", err.Error())
		return err
	}

	logDebugf("shutdown old exporter instance")
	// if older one exists, close and unregister it
	if PgExporter != nil {
		// DO NOT MANUALLY CLOSE OLD EXPORTER INSTANCE because the stupid implementation of sql.DB
		// there connection will be automatically released after 1 min
		// PgExporter.Close()
		prometheus.Unregister(PgExporter)
	}
	PgExporter = newExporter
	runtime.GC()
	logInfof("server reloaded")
	return nil
}

// DummyServer response with a dummy metrics pg_up 0 or pgbouncer_up 0
func DummyServer() (s *http.Server, exit <-chan bool) {
	mux := http.NewServeMux()
	dummyMetricName := `pg_up`
	if ParseDatname(*pgURL) == `pgbouncer` {
		dummyMetricName = `pgbouncer_up`
	}
	mux.HandleFunc(*metricPath, func(w http.ResponseWriter, req *http.Request) {
		_, _ = fmt.Fprintf(w, "# HELP %s last scrape was able to connect to the server: 1 for yes, 0 for no\n# TYPE %s gauge\n%s 0", dummyMetricName, dummyMetricName, dummyMetricName)
	})

	listenAddr := (*webConfig.WebListenAddresses)[0]
	httpServer := &http.Server{
		Addr:    listenAddr,
		Handler: mux,
	}
	exitChan := make(chan bool, 1)
	go func() {
		if err := httpServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			logDebugf("shutdown dummy server")
		}
		exitChan <- true
	}()
	return httpServer, exitChan
}

// Run pg_exporter
func Run() {
	ParseArgs()

	// explain config only
	if *dryRun {
		DryRun()
	}

	if *configPath == "" {
		Logger.Error("no valid config path, exit")
		os.Exit(1)
	}

	if len(*webConfig.WebListenAddresses) == 0 {
		Logger.Error("invalid listen address", "addresses", *webConfig.WebListenAddresses)
		os.Exit(1)
	}
	listenAddr := (*webConfig.WebListenAddresses)[0]

	// DummyServer will server a constant pg_up
	// launch a dummy server to check listen address availability
	// and fake a pg_up 0 metrics before PgExporter connecting to target instance
	// otherwise, exporter API is not available until target instance online
	dummySrv, closeChan := DummyServer()

	// create exporter: if target is down, exporter creation will wait until it backup online
	var err error
	PgExporter, err = NewExporter(
		*pgURL,
		WithConfig(*configPath),
		WithConstLabels(*constLabels),
		WithCacheDisabled(*disableCache),
		WithFailFast(*failFast),
		WithNamespace(*exporterNamespace),
		WithAutoDiscovery(*autoDiscovery),
		WithExcludeDatabase(*excludeDatabase),
		WithIncludeDatabase(*includeDatabase),
		WithTags(*serverTags),
		WithConnectTimeout(*connectTimeout),
	)
	if err != nil {
		logFatalf("fail creating pg_exporter: %s", err.Error())
		os.Exit(2)
	}

	// trigger a manual planning before explain
	if *explainOnly {
		PgExporter.server.Plan()
		fmt.Println(PgExporter.Explain())
		os.Exit(0)
	}

	prometheus.MustRegister(PgExporter)
	defer PgExporter.Close()

	// reload conf when receiving SIGHUP or SIGUSR1
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGHUP)
	go func() {
		for sig := range sigs {
			switch sig {
			case syscall.SIGHUP:
				logInfof("%v received, reloading", sig)
				_ = Reload()
			}
		}
	}()

	/* ================ REST API ================ */
	// basic
	http.HandleFunc("/", TitleFunc)
	http.HandleFunc("/version", VersionFunc)
	// reload
	http.HandleFunc("/reload", ReloadFunc)
	// explain & stat
	http.HandleFunc("/stat", PgExporter.StatFunc)
	http.HandleFunc("/explain", PgExporter.ExplainFunc)
	// alive
	http.HandleFunc("/up", PgExporter.UpCheckFunc)
	http.HandleFunc("/read", PgExporter.UpCheckFunc)
	http.HandleFunc("/health", PgExporter.UpCheckFunc)
	http.HandleFunc("/liveness", PgExporter.UpCheckFunc)
	http.HandleFunc("/readiness", PgExporter.UpCheckFunc)
	// primary
	http.HandleFunc("/primary", PgExporter.PrimaryCheckFunc)
	http.HandleFunc("/leader", PgExporter.PrimaryCheckFunc)
	http.HandleFunc("/master", PgExporter.PrimaryCheckFunc)
	http.HandleFunc("/read-write", PgExporter.PrimaryCheckFunc)
	http.HandleFunc("/rw", PgExporter.PrimaryCheckFunc)
	// replica
	http.HandleFunc("/replica", PgExporter.ReplicaCheckFunc)
	http.HandleFunc("/standby", PgExporter.ReplicaCheckFunc)
	http.HandleFunc("/slave", PgExporter.ReplicaCheckFunc)
	http.HandleFunc("/read-only", PgExporter.ReplicaCheckFunc)
	http.HandleFunc("/ro", PgExporter.ReplicaCheckFunc)

	// metric
	_ = dummySrv.Close()
	<-closeChan
	http.Handle(*metricPath, promhttp.Handler())

	logInfof("pg_exporter for %s start, listen on %s%s", ShadowPGURL(*pgURL), listenAddr, *metricPath)

	srv := &http.Server{}
	if err := web.ListenAndServe(srv, webConfig, Logger); err != nil {
		logFatalf("http server failed: %s", err.Error())
	}

}
