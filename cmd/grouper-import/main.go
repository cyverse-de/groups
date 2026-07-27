// Command grouper-import copies DE group data out of Grouper and into the
// permissions schema of the DE database.
//
// It is a standalone command rather than a migration: it reads a database that
// does not exist in every environment, and keeping it out of the migration chain
// means a rollback removes structure without destroying group data while Grouper
// is still the recoverable source of truth.
//
// The import is convergent, not merely idempotent. It is expected to run many
// times while Grouper is still taking writes, so membership and grants are
// reconciled to Grouper's current state -- including removals -- rather than
// accumulated. It never deletes groups: a group that disappears from Grouper is
// reported, because deleting one cascades away every permission granted to it.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/cyverse-de/groups/store/pgstore"
)

// envPrefix namespaces the environment variables that back the flags, matching
// the groups service's own GROUPS_ prefix.
const envPrefix = "GROUPS_IMPORT_"

func main() {
	cfg, err := configFromArgs()
	if err != nil {
		fmt.Fprintf(os.Stderr, "grouper-import: %s\n", err)
		os.Exit(2)
	}

	fmt.Fprintf(os.Stderr, "grouper-import: phase=%s prefix=%s dry-run=%t\n",
		cfg.Phase, cfg.Prefix, cfg.DryRun)

	// The import itself is added phase by phase; the parser and configuration
	// are in place and covered by tests.
	fmt.Fprintln(os.Stderr, "grouper-import: no phase is implemented yet")
	os.Exit(1)
}

// configFromArgs builds and validates the configuration from flags, falling back
// to environment variables so that credentials need not appear in a command line
// where anyone running ps can read them.
func configFromArgs() (*config, error) {
	var cfg config

	fs := flag.NewFlagSet("grouper-import", flag.ContinueOnError)
	fs.StringVar(&cfg.GrouperURI, "grouper-uri", env("GROUPER_URI", ""),
		"connection URI for the Grouper database to read")
	fs.StringVar(&cfg.Prefix, "grouper-prefix", env("GROUPER_PREFIX", ""),
		"Grouper folder scoping the import to one environment, e.g. iplant:de:prod (required)")
	fs.StringVar(&cfg.DatabaseURI, "db-uri", env("DB_URI", ""),
		"connection URI for the DE database")
	fs.StringVar(&cfg.Schema, "db-schema", env("DB_SCHEMA", "permissions"),
		"schema holding the permissions and group tables")
	fs.StringVar(&cfg.UserSuffix, "user-suffix", env("USER_SUFFIX", pgstore.DefaultUserSuffix),
		"domain appended to a bare username to form a DE username")
	fs.StringVar(&cfg.Permissions, "permissions-base", env("PERMISSIONS_BASE", "http://permissions"),
		"base URL of the permissions service")
	fs.StringVar(&cfg.Phase, "phase", env("PHASE", phaseAll),
		"which half to run: groups, permissions, or all")
	fs.BoolVar(&cfg.DryRun, "dry-run", env("DRY_RUN", "") != "",
		"report what would change without writing anything")

	if err := fs.Parse(os.Args[1:]); err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// env reads a namespaced environment variable, falling back to def.
func env(name, def string) string {
	if v, ok := os.LookupEnv(envPrefix + name); ok {
		return strings.TrimSpace(v)
	}
	return def
}
