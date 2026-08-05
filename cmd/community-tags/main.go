// Command community-tags rewrites the community tags on DE apps so that they
// name a community by its ID rather than by its name.
//
// The tag is an AVU on the app, and its value was minted by the browser from the
// community's display name -- which in Grouper was the full colon-delimited
// path. A name is not a stable identifier: renaming a community left every app
// tagged with the old name out of its own listing, silently, which is how
// production came to hold tags naming communities that no longer exist.
//
// It is a standalone command rather than a migration because the mapping spans
// two databases: community identity lives in the permissions schema of the DE
// database, and the tags live in the metadata service's own database. No
// migration in either can see both.
//
// Running it twice is a no-op: a value that is already a community ID is left
// alone.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// envPrefix namespaces the environment variables backing the flags, matching the
// groups service's own GROUPS_ prefix.
const envPrefix = "GROUPS_TAGS_"

func main() {
	cfg, err := configFromArgs()
	if err != nil {
		fmt.Fprintf(os.Stderr, "community-tags: %s\n", err)
		os.Exit(2)
	}

	logf("attribute=%q dry-run=%t", cfg.Attribute, cfg.DryRun)

	rep, err := run(context.Background(), cfg)
	if rep != nil {
		rep.Print()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "community-tags: %s\n", err)
		os.Exit(1)
	}
	if cfg.DryRun {
		logf("dry run: nothing was written")
	}
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "community-tags: "+format+"\n", args...)
}

// config carries everything the command needs, validated before any connection
// is opened.
type config struct {
	// DatabaseURI is the DE database holding the permissions schema, which is
	// where community identity lives.
	DatabaseURI string
	Schema      string

	// MetadataURI is the metadata service's database, which holds the AVUs.
	MetadataURI string

	// Attribute is the AVU attribute the community tag is stored under. It must
	// match apps' workspace.metadata.communities.attr, or this rewrites nothing
	// and reports a clean run.
	Attribute string

	DryRun bool
}

func (c *config) validate() error {
	var errs []string
	if c.DatabaseURI == "" {
		errs = append(errs, "db-uri must be set: the DE database holding the permissions schema")
	}
	if c.MetadataURI == "" {
		errs = append(errs, "metadata-uri must be set: the metadata service's database")
	}
	if c.Attribute == "" {
		errs = append(errs, "attribute must be set: the AVU attribute community tags are stored under")
	}
	if len(errs) > 0 {
		return fmt.Errorf("invalid configuration:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}

func configFromArgs() (*config, error) {
	var cfg config

	fs := flag.NewFlagSet("community-tags", flag.ContinueOnError)
	fs.StringVar(&cfg.DatabaseURI, "db-uri", env("DB_URI", ""),
		"connection URI for the DE database")
	fs.StringVar(&cfg.Schema, "db-schema", env("DB_SCHEMA", "permissions"),
		"schema holding the group tables")
	fs.StringVar(&cfg.MetadataURI, "metadata-uri", env("METADATA_URI", ""),
		"connection URI for the metadata database")
	fs.StringVar(&cfg.Attribute, "attribute", env("ATTRIBUTE", "cyverse-community"),
		"AVU attribute that community tags are stored under")
	dryRun, err := envBool("DRY_RUN")
	if err != nil {
		return nil, err
	}
	fs.BoolVar(&cfg.DryRun, "dry-run", dryRun,
		"report what would change without writing anything")

	if err := fs.Parse(os.Args[1:]); err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func env(name, def string) string {
	if v, ok := os.LookupEnv(envPrefix + name); ok {
		return strings.TrimSpace(v)
	}
	return def
}

// envBool reads a namespaced boolean environment variable with ParseBool
// semantics, so "false" and "0" disable rather than counting as set. An
// unparsable value is an error: guessing either way on a dry-run flag is worse
// than refusing to start.
func envBool(name string) (bool, error) {
	v := env(name, "")
	if v == "" {
		return false, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s%s must be a boolean (true/false/1/0), not %q", envPrefix, name, v)
	}
	return b, nil
}
