package main

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/cyverse-de/groups/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const prodPrefix = "iplant:de:prod"

// TestParseGroupName covers every Grouper name shape in production. All 2,583
// production names fall into the five below; anything else must abort the import
// rather than be skipped, because the obvious lazy handling produces a clean run
// with missing groups.
func TestParseGroupName(t *testing.T) {
	tests := []struct {
		name string
		// full is the Grouper group name.
		full string

		wantType    string
		wantOwner   string
		wantName    string
		wantTrimmed bool
		wantErr     bool
	}{
		{
			name:      "a collaborator list",
			full:      prodPrefix + ":users:jxl1269:collaborator-lists:Case Western Reserve University",
			wantType:  model.GroupTypeCollaboratorList,
			wantOwner: "jxl1269",
			wantName:  "Case Western Reserve University",
		},
		{
			name:      "a collaborator list with the usual name",
			full:      prodPrefix + ":users:aramsey:collaborator-lists:default",
			wantType:  model.GroupTypeCollaboratorList,
			wantOwner: "aramsey",
			wantName:  "default",
		},
		{
			name:      "a team",
			full:      prodPrefix + ":teams:mszens:MADI-Core2",
			wantType:  model.GroupTypeTeam,
			wantOwner: "mszens",
			wantName:  "MADI-Core2",
		},
		{
			name:     "a community",
			full:     prodPrefix + ":communities:NEON",
			wantType: model.GroupTypeCommunity,
			wantName: "NEON",
		},
		{
			name:     "the de-users system group",
			full:     prodPrefix + ":users:de-users",
			wantType: model.GroupTypeSystem,
			wantName: "de-users",
		},
		{
			name:     "the workshop-users system group",
			full:     prodPrefix + ":users:workshop-users",
			wantType: model.GroupTypeSystem,
			wantName: "workshop-users",
		},
		{
			// Production holds exactly two of these.
			name:        "a name padded with whitespace is trimmed and reported",
			full:        prodPrefix + ":communities:Jemez Ecosystem ",
			wantType:    model.GroupTypeCommunity,
			wantName:    "Jemez Ecosystem",
			wantTrimmed: true,
		},
		{
			name:     "an apostrophe and spaces are ordinary characters",
			full:     prodPrefix + ":communities:paul's collection",
			wantType: model.GroupTypeCommunity,
			wantName: "paul's collection",
		},
		{
			// The system rule must be tried before the per-user rule, or a user
			// named de-users would shadow the system group.
			name:      "a user named de-users still gets a collaborator list",
			full:      prodPrefix + ":users:de-users:collaborator-lists:default",
			wantType:  model.GroupTypeCollaboratorList,
			wantOwner: "de-users",
			wantName:  "default",
		},
		{
			// One such group exists under iplant:de:dev, and importing it
			// alongside the prod one would collide on (group_type, owner, name).
			name:    "a group from another environment is not ours",
			full:    "iplant:de:dev:users:de-users",
			wantErr: true,
		},
		{name: "an unrecognized shape", full: prodPrefix + ":something:else", wantErr: true},
		{name: "the folder itself", full: prodPrefix, wantErr: true},
		{name: "empty", full: "", wantErr: true},
		{
			// Cannot happen in production, but groups.name CHECKs against colons,
			// so a name that would violate it must abort rather than fail on insert.
			name:    "a colon in the short name",
			full:    prodPrefix + ":teams:bob:project:phase2",
			wantErr: true,
		},
		{
			name:    "a blank short name",
			full:    prodPrefix + ":communities:   ",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseGroupName(prodPrefix, tt.full)
			if tt.wantErr {
				require.Error(t, err)
				assert.True(t, errors.Is(err, errUnparsedName),
					"an unparsable name must be reported as such so the import can abort")
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantType, got.GroupType)
			assert.Equal(t, tt.wantOwner, got.Owner)
			assert.Equal(t, tt.wantName, got.Name)
			assert.Equal(t, tt.wantTrimmed, got.Trimmed)
			assert.Equal(t, tt.full, got.LegacyName, "the original name is kept for provenance")
		})
	}
}

// TestParseGroupNameRequiresPrefix guards the scoping rule: the environment
// prefix is required, never defaulted, because iplant:de:% spans environments.
func TestParseGroupNameRequiresPrefix(t *testing.T) {
	_, err := parseGroupName("", prodPrefix+":communities:NEON")
	require.Error(t, err)
	assert.NotErrorIs(t, err, errUnparsedName,
		"a missing prefix is a configuration error, not an unparsable name")
}

// TestParseGroupNameAgainstInventory parses a real Grouper name inventory, so
// the parser can be checked against production before an import is attempted.
// Dump one with:
//
//	psql -h <host> -U GrouperSystem -d grouper -Atc \
//	  "select name from grouper_groups where name like 'iplant:de:prod:%';" > names.txt
//	GROUPER_NAMES_FILE=names.txt GROUPER_NAME_PREFIX=iplant:de:prod go test ./cmd/grouper-import/
func TestParseGroupNameAgainstInventory(t *testing.T) {
	path := os.Getenv("GROUPER_NAMES_FILE")
	if path == "" {
		t.Skip("GROUPER_NAMES_FILE is not set")
	}
	prefix := os.Getenv("GROUPER_NAME_PREFIX")
	require.NotEmpty(t, prefix, "GROUPER_NAME_PREFIX must be set alongside GROUPER_NAMES_FILE")

	content, err := os.ReadFile(path)
	require.NoError(t, err)

	byType := map[string]int{}
	var trimmed, failed []string
	for _, line := range strings.Split(strings.TrimRight(string(content), "\n"), "\n") {
		if line == "" {
			continue
		}
		parsed, err := parseGroupName(prefix, line)
		if err != nil {
			failed = append(failed, line)
			continue
		}
		byType[parsed.GroupType]++
		if parsed.Trimmed {
			trimmed = append(trimmed, line)
		}
	}

	t.Logf("parsed by type: %v", byType)
	t.Logf("short names trimmed: %d %v", len(trimmed), trimmed)
	assert.Empty(t, failed, "every name in the inventory must parse")
}
