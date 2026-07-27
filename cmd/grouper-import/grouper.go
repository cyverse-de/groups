package main

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/lib/pq"
)

// grouperSource reads DE group data from a Grouper database. Every query is
// read-only: Grouper remains the source of truth until it is decommissioned, and
// the importer must never be the thing that changes it.
type grouperSource struct {
	db     *sql.DB
	prefix string
}

// openGrouper connects to the Grouper database and verifies the connection.
func openGrouper(ctx context.Context, uri, prefix string) (*grouperSource, error) {
	db, err := sql.Open("postgres", uri)
	if err != nil {
		return nil, fmt.Errorf("could not open the Grouper database: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("could not reach the Grouper database; check grouper.uri: %w", err)
	}
	return &grouperSource{db: db, prefix: prefix}, nil
}

func (g *grouperSource) Close() error { return g.db.Close() }

// scope is the LIKE pattern restricting every query to one environment.
func (g *grouperSource) scope() string { return g.prefix + ":%" }

// grouperGroup is a group as Grouper holds it.
type grouperGroup struct {
	// ID is Grouper's 32-hex identifier, preserved as the group's external ID so
	// existing permission grants and iRODS @grouper-<id> names stay valid.
	ID          string
	Name        string
	Description string
}

// Groups returns every DE group under the configured environment prefix.
func (g *grouperSource) Groups(ctx context.Context) ([]grouperGroup, error) {
	rows, err := g.db.QueryContext(ctx, `
		SELECT id, name, coalesce(description, '')
		  FROM grouper_groups
		 WHERE name LIKE $1
		 ORDER BY name`, g.scope())
	if err != nil {
		return nil, fmt.Errorf("could not read Grouper groups: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var groups []grouperGroup
	for rows.Next() {
		var gg grouperGroup
		if err := rows.Scan(&gg.ID, &gg.Name, &gg.Description); err != nil {
			return nil, fmt.Errorf("could not read a Grouper group: %w", err)
		}
		groups = append(groups, gg)
	}
	return groups, rows.Err()
}

// grouperMember is one direct membership.
type grouperMember struct {
	GroupName string
	SubjectID string
	// IsGroup reports a nested group rather than a user. Production has 152 of
	// these, so nesting must survive the import.
	IsGroup bool
}

// Members returns direct group membership.
//
// Only `immediate` rows are read. The view also returns `effective` rows, which
// are Grouper's own expansion of nested membership; importing those would record
// derived facts as direct ones, and the expansion is recomputed on this side
// anyway. The diff between the two is the closure check.
//
// DISTINCT because the unfiltered view returns some pairs by more than one path.
func (g *grouperSource) Members(ctx context.Context, grouperAdminUser string) ([]grouperMember, error) {
	rows, err := g.db.QueryContext(ctx, `
		SELECT DISTINCT group_name, subject_id, subject_source
		  FROM grouper_memberships_v
		 WHERE group_name LIKE $1
		   AND list_name = 'members'
		   AND membership_type = 'immediate'
		   AND subject_id <> $2
		 ORDER BY group_name, subject_id`, g.scope(), grouperAdminUser)
	if err != nil {
		return nil, fmt.Errorf("could not read Grouper membership: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var members []grouperMember
	for rows.Next() {
		var m grouperMember
		var source string
		if err := rows.Scan(&m.GroupName, &m.SubjectID, &source); err != nil {
			return nil, fmt.Errorf("could not read a Grouper membership: %w", err)
		}
		switch source {
		case sourceLDAP:
		case sourceGroup:
			m.IsGroup = true
		default:
			// Abort rather than guess: a source we have not seen could be a user
			// or a group, and choosing wrong silently corrupts membership.
			return nil, fmt.Errorf("unknown member subject source %q for %q in %q",
				source, m.SubjectID, m.GroupName)
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

// EffectiveMembers returns Grouper's own transitive expansion, used to check the
// closure this side computes. Users only, matching group_effective_members.
func (g *grouperSource) EffectiveMembers(ctx context.Context, grouperAdminUser string) ([]grouperMember, error) {
	rows, err := g.db.QueryContext(ctx, `
		SELECT DISTINCT group_name, subject_id
		  FROM grouper_memberships_v
		 WHERE group_name LIKE $1
		   AND list_name = 'members'
		   AND subject_source = $2
		   AND subject_id <> $3
		 ORDER BY group_name, subject_id`, g.scope(), sourceLDAP, grouperAdminUser)
	if err != nil {
		return nil, fmt.Errorf("could not read Grouper effective membership: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var members []grouperMember
	for rows.Next() {
		var m grouperMember
		if err := rows.Scan(&m.GroupName, &m.SubjectID); err != nil {
			return nil, fmt.Errorf("could not read a Grouper effective membership: %w", err)
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

// MemberCounts returns the number of direct members per group name, used to
// decide which side of an identity collision carries anything worth keeping.
func (g *grouperSource) MemberCounts(ctx context.Context) (map[string]int, error) {
	rows, err := g.db.QueryContext(ctx, `
		SELECT group_name, count(DISTINCT subject_id)
		  FROM grouper_memberships_v
		 WHERE group_name LIKE $1
		   AND list_name = 'members'
		   AND membership_type = 'immediate'
		 GROUP BY group_name`, g.scope())
	if err != nil {
		return nil, fmt.Errorf("could not count Grouper membership: %w", err)
	}
	defer func() { _ = rows.Close() }()

	counts := map[string]int{}
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			return nil, fmt.Errorf("could not read a Grouper membership count: %w", err)
		}
		counts[name] = n
	}
	return counts, rows.Err()
}

// grouperPrivilege is one privilege held on a group.
type grouperPrivilege struct {
	GroupName string
	ListName  string
	SubjectID string
	Source    string
}

// Privileges returns the immediate privileges held on DE groups. Effective ones
// are excluded for the reason given on classifyPrivilege.
func (g *grouperSource) Privileges(ctx context.Context) ([]grouperPrivilege, error) {
	rows, err := g.db.QueryContext(ctx, `
		SELECT DISTINCT group_name, list_name, subject_id, subject_source
		  FROM grouper_memberships_v
		 WHERE group_name LIKE $1
		   AND list_name <> 'members'
		   AND membership_type = 'immediate'
		 ORDER BY group_name, list_name, subject_id`, g.scope())
	if err != nil {
		return nil, fmt.Errorf("could not read Grouper privileges: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var privileges []grouperPrivilege
	for rows.Next() {
		var p grouperPrivilege
		if err := rows.Scan(&p.GroupName, &p.ListName, &p.SubjectID, &p.Source); err != nil {
			return nil, fmt.Errorf("could not read a Grouper privilege: %w", err)
		}
		privileges = append(privileges, p)
	}
	return privileges, rows.Err()
}
