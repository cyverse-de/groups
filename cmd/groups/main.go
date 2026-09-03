package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cyverse-de/go-mod/cfg"
	"github.com/cyverse-de/go-mod/logging"
	"github.com/sirupsen/logrus"
)

const serviceName = "groups"

// shutdownTimeout bounds how long in-flight requests have to finish after a
// SIGTERM. It stays under Kubernetes' 30-second default grace period, so the
// resources released afterwards are released before the pod is killed.
const shutdownTimeout = 20 * time.Second

var log = logging.Log.WithFields(logrus.Fields{"service": serviceName})

func main() {
	var (
		configPath = flag.String("config", cfg.DefaultConfigPath, "Path to the config file")
		dotEnvPath = flag.String("dotenv-path", cfg.DefaultDotEnvPath, "Path to the dotenv file")
		envPrefix  = flag.String("env-prefix", "GROUPS_", "The prefix for environment variables")
		logLevel   = flag.String("log-level", "info", "One of trace, debug, info, warn, error, fatal, or panic.")
		listenPort = flag.Int("port", 60000, "The port the service listens on for requests")
	)
	flag.Parse()
	logging.SetupLogging(*logLevel)

	l := log.WithField("context", "main")

	config, err := cfg.Init(&cfg.Settings{
		EnvPrefix:   *envPrefix,
		ConfigPath:  *configPath,
		DotEnvPath:  *dotEnvPath,
		StrictMerge: false,
		FileType:    cfg.YAML,
	})
	if err != nil {
		l.Fatal(err)
	}
	l.Infof("done reading config from %s", *configPath)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app, err := NewApp(ctx, config)
	if err != nil {
		l.Fatal(err)
	}

	addr := fmt.Sprintf(":%d", *listenPort)
	srv := &http.Server{
		Addr:    addr,
		Handler: app.Router(),
		// Bounds how long a client may dribble request headers, so idle
		// half-open connections cannot pin goroutines.
		ReadHeaderTimeout: 30 * time.Second,
	}

	served := make(chan error, 1)
	go func() { served <- srv.ListenAndServe() }()
	l.Infof("listening on %s", addr)

	failed := false
	select {
	case err := <-served:
		// ListenAndServe never returns nil. l.Fatal would os.Exit before any
		// cleanup ran, so log, release resources, and exit nonzero explicitly.
		l.Errorf("the HTTP server stopped: %s", err)
		failed = true
	case <-ctx.Done():
		// Restore the default signal handling, so a second signal can still kill
		// a shutdown that will not finish.
		stop()
		l.Infof("shutting down, draining in-flight requests for up to %s", shutdownTimeout)

		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			l.Errorf("the HTTP server did not shut down cleanly: %s", err)
			failed = true
		}
	}

	if err := app.Close(); err != nil {
		l.Errorf("error releasing resources on shutdown: %s", err)
		failed = true
	}
	if failed {
		os.Exit(1)
	}
}
