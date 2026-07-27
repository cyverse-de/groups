package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/cyverse-de/groups/model"
)

// errUnparsedName marks a Grouper name that does not match a known shape. The
// import aborts on it rather than skipping: every lazy handling of an unknown
// name produces a clean-looking run with groups silently missing.
var errUnparsedName = errors.New("unparsed group name")

// collaboratorListSegment is the fixed folder Grouper puts a user's
// collaborator lists under.
const collaboratorListSegment = "collaborator-lists"

// systemGroupNames are the groups the DE manages for itself. They are matched
// before the per-user rules so that a user named `de-users` cannot shadow one.
var systemGroupNames = map[string]struct{}{
	"de-users":       {},
	"workshop-users": {},
}

// ParsedName is a Grouper group name resolved into the structured identity the
// permissions schema stores.
type ParsedName struct {
	GroupType string
	Owner     string
	Name      string

	// LegacyName is the original Grouper name, kept in groups.legacy_name for
	// provenance and for reconciling against Grouper before it is switched off.
	LegacyName string

	// Trimmed reports that the short name was padded with whitespace, which the
	// importer must report: two production communities are.
	Trimmed bool
}

// parseGroupName resolves a Grouper group name under the given environment
// prefix (for example "iplant:de:prod").
//
// The prefix is required and never defaulted. Scoping to `iplant:de:%` instead
// would pull in a stray `users:de-users` from another environment, which
// collides with the real one on (group_type, owner, name).
func parseGroupName(prefix, full string) (*ParsedName, error) {
	if prefix == "" {
		return nil, errors.New("the Grouper environment prefix must be configured")
	}

	rest, ok := strings.CutPrefix(full, prefix+":")
	if !ok {
		return nil, fmt.Errorf("%w: %q is not under %q", errUnparsedName, full, prefix)
	}

	parsed, err := parseRest(rest)
	if err != nil {
		return nil, err
	}
	parsed.LegacyName = full

	trimmed := strings.TrimSpace(parsed.Name)
	switch {
	case trimmed == "":
		return nil, fmt.Errorf("%w: %q has a blank short name", errUnparsedName, full)
	case strings.Contains(trimmed, ":"):
		// groups.name rejects colons, so this would fail on insert. Reporting it
		// here names the group that caused it.
		return nil, fmt.Errorf("%w: the short name of %q contains a colon", errUnparsedName, full)
	}
	parsed.Trimmed = trimmed != parsed.Name
	parsed.Name = trimmed

	return parsed, nil
}

// parseRest dispatches on the portion of the name below the environment prefix.
func parseRest(rest string) (*ParsedName, error) {
	segments := strings.Split(rest, ":")

	switch {
	// users:<user>:collaborator-lists:<short>
	case len(segments) >= 4 && segments[0] == "users" && segments[2] == collaboratorListSegment:
		return &ParsedName{
			GroupType: model.GroupTypeCollaboratorList,
			Owner:     segments[1],
			Name:      strings.Join(segments[3:], ":"),
		}, nil

	// users:<system group>
	case len(segments) == 2 && segments[0] == "users":
		if _, ok := systemGroupNames[segments[1]]; !ok {
			break
		}
		return &ParsedName{GroupType: model.GroupTypeSystem, Name: segments[1]}, nil

	// teams:<creator>:<short>
	case len(segments) >= 3 && segments[0] == "teams":
		return &ParsedName{
			GroupType: model.GroupTypeTeam,
			Owner:     segments[1],
			Name:      strings.Join(segments[2:], ":"),
		}, nil

	// communities:<short>
	case len(segments) == 2 && segments[0] == "communities":
		return &ParsedName{GroupType: model.GroupTypeCommunity, Name: segments[1]}, nil
	}

	return nil, fmt.Errorf("%w: %q", errUnparsedName, rest)
}
