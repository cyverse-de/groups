package main

import (
	"flag"
	"fmt"
	"net/http"

	"github.com/cyverse-de/go-mod/cfg"
	"github.com/cyverse-de/go-mod/logging"
	"github.com/sirupsen/logrus"
)

const serviceName = "groups"

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

	app, err := NewApp(config)
	if err != nil {
		l.Fatal(err)
	}

	addr := fmt.Sprintf(":%d", *listenPort)
	l.Infof("listening on %s", addr)
	l.Fatal(http.ListenAndServe(addr, app.Router()))
}
