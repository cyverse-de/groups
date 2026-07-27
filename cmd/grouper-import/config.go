package main

import (
	"errors"
	"fmt"
	"strings"
)

// The phases of the import. They are separable because group data is written in
// one transaction that --dry-run can roll back, while permission grants go
// through the permissions HTTP API and cannot be.
const (
	phaseGroups      = "groups"
	phasePermissions = "permissions"
	phaseAll         = "all"
)

// config carries everything the importer needs. Every setting is validated
// before any connection is opened, so a misconfigured run fails immediately
// rather than part-way through writing rows.
type config struct {
	// GrouperURI is the Grouper database to read from.
	GrouperURI string

	// Prefix scopes the import to one environment's folder, for example
	// "iplant:de:prod". Required and never defaulted.
	Prefix string

	// DatabaseURI is the DE database holding the permissions schema.
	DatabaseURI string
	Schema      string

	// UserSuffix forms a DE username from a bare one when correlating a subject
	// with its public.users row.
	UserSuffix string

	// Permissions is the base URL of the permissions service, which owns
	// resource and subject creation for grants.
	Permissions string

	Phase  string
	DryRun bool
}

// validate checks the configuration and normalizes what it can, reporting the
// setting responsible for any failure.
func (c *config) validate() error {
	// A trailing colon would be doubled when the prefix is matched, making every
	// name in the inventory fail to parse.
	c.Prefix = strings.TrimSuffix(strings.TrimSpace(c.Prefix), ":")

	var errs []error
	if c.GrouperURI == "" {
		errs = append(errs, errors.New("grouper.uri must be set: the Grouper database to import from"))
	}
	if c.Prefix == "" {
		errs = append(errs, errors.New(
			"grouper.prefix must be set to the environment folder, for example iplant:de:prod; "+
				"it is required because a wider scope imports another environment's de-users, "+
				"which collides with this one"))
	}
	if c.DatabaseURI == "" {
		errs = append(errs, errors.New("db.uri must be set: the DE database holding the permissions schema"))
	}
	if c.UserSuffix == "" {
		errs = append(errs, errors.New(
			"users.suffix must be set: without it, imported subjects cannot be correlated with DE users"))
	}
	if c.Permissions == "" && c.Phase != phaseGroups {
		errs = append(errs, errors.New(
			"permissions.base must be set: privilege grants go through the permissions service"))
	}

	switch c.Phase {
	case phaseGroups, phasePermissions, phaseAll:
	default:
		errs = append(errs, fmt.Errorf("phase must be one of %q, %q, or %q, not %q",
			phaseGroups, phasePermissions, phaseAll, c.Phase))
	}

	return errors.Join(errs...)
}
