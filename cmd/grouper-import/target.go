package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/cyverse-de/groups/store/pgstore"
	"github.com/lib/pq"
)

// importLockID namespaces the advisory lock that keeps two imports from
// interleaving. Arbitrary but fixed.
const importLockID = 0x6772705f696d7031 // "grp_imp1"

// errNotAuthoritative reports that Grouper is no longer the source of truth, so
// a reconciling import would delete data created natively.
var errNotAuthoritative = errors.New("grouper is no longer the authoritative source of group data")

// target is the DE database the import writes to.
type target struct {
	db     *sql.DB
	suffix string
}

// openTarget connects to the DE database with the group schema on the search
// path.
func openTarget(ctx context.Context, uri, schema, suffix string) (*target, error) {
	dsn, err := pgstore.DSNWithSearchPath(uri, schema)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("could not open the DE database: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("could not reach the DE database; check db.uri: %w", err)
	}
	return &target{db: db, suffix: suffix}, nil
}

func (t *target) Close() error { return t.db.Close() }

// RequireGrouperAuthoritative refuses to run unless group_data_source still says
// Grouper owns group data.
//
// The import reconciles: it removes memberships and grants Grouper no longer
// has. After cutover that is destructive, so this is a hard stop with no
// override flag. Setting the marker back is the deliberate, attributed act that
// re-enables it.
func (t *target) RequireGrouperAuthoritative(ctx context.Context) error {
	var source, changedBy string
	err := t.db.QueryRowContext(ctx,
		`SELECT source, changed_by FROM group_data_source`).Scan(&source, &changedBy)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w: group_data_source has no row; migration 000054 seeds one", errNotAuthoritative)
	case err != nil:
		return fmt.Errorf("could not read group_data_source: %w", err)
	case source != "grouper":
		return fmt.Errorf("%w: group_data_source says %q (set by %s). "+
			"Reconciling now would delete group data created since cutover. "+
			"To re-enable the import, set it back to 'grouper' explicitly",
			errNotAuthoritative, source, changedBy)
	}
	return nil
}

// Lock takes the advisory lock for the duration of the import, returning a
// release function. Session-scoped so it covers work spanning transactions.
func (t *target) Lock(ctx context.Context) (func(), error) {
	conn, err := t.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not acquire a connection for the import lock: %w", err)
	}

	var locked bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, importLockID).Scan(&locked); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("could not take the import lock: %w", err)
	}
	if !locked {
		_ = conn.Close()
		return nil, errors.New("another import is already running (advisory lock held)")
	}

	return func() {
		if _, err := conn.ExecContext(context.WithoutCancel(ctx),
			`SELECT pg_advisory_unlock($1)`, importLockID); err != nil {
			logf("could not release the import lock: %s", err)
		}
		_ = conn.Close()
	}, nil
}

// targetGroup is a group already present in the DE database.
type targetGroup struct {
	ID         string
	GroupType  string
	Owner      string
	Name       string
	LegacyName string
}

// Groups returns every imported group, keyed by external ID. Groups created
// natively have no legacy name and are reported so that the caller can tell
// "deleted from Grouper" from "never came from Grouper".
func (t *target) Groups(ctx context.Context) (map[string]targetGroup, error) {
	rows, err := t.db.QueryContext(ctx, `
		SELECT s.subject_id, g.group_type, coalesce(g.owner, ''), g.name, coalesce(g.legacy_name, '')
		  FROM groups g
		  JOIN subjects s ON s.id = g.subject_id`)
	if err != nil {
		return nil, fmt.Errorf("could not read existing groups: %w", err)
	}
	defer func() { _ = rows.Close() }()

	groups := map[string]targetGroup{}
	for rows.Next() {
		var g targetGroup
		if err := rows.Scan(&g.ID, &g.GroupType, &g.Owner, &g.Name, &g.LegacyName); err != nil {
			return nil, fmt.Errorf("could not read an existing group: %w", err)
		}
		groups[g.ID] = g
	}
	return groups, rows.Err()
}

// UpsertGroup creates or updates a group, preserving Grouper's identifier as the
// external subject ID.
//
// This is why the importer writes group rows itself rather than going through
// the groups service: the service mints a new identifier, and an imported group
// must keep Grouper's, or 3,323 existing permission grants detach and every
// iRODS @grouper-<id> name changes.
func (t *target) UpsertGroup(ctx context.Context, tx *sql.Tx, grouperID string, parsed *ParsedName, description string) error {
	var internalID string
	err := tx.QueryRowContext(ctx, `
		INSERT INTO subjects (subject_id, subject_type) VALUES ($1, 'group')
		ON CONFLICT (subject_id) DO UPDATE SET subject_type = subjects.subject_type
		RETURNING id`, grouperID).Scan(&internalID)
	if err != nil {
		return fmt.Errorf("could not create the subject for group %q: %w", parsed.LegacyName, err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO groups (subject_id, group_type, owner, name, description, legacy_name)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (subject_id) DO UPDATE
		   SET group_type = EXCLUDED.group_type,
		       owner = EXCLUDED.owner,
		       name = EXCLUDED.name,
		       description = EXCLUDED.description,
		       legacy_name = EXCLUDED.legacy_name`,
		internalID, parsed.GroupType, nullString(parsed.Owner), parsed.Name,
		description, parsed.LegacyName)
	if err != nil {
		return fmt.Errorf("could not write group %q: %w", parsed.LegacyName, err)
	}
	return nil
}

// BackfillUserIDs correlates user subjects with their DE user rows. Run after
// membership, when every imported member has a subject row.
//
// Matching is on the fully suffixed username. A bare-name match would be
// ambiguous: production public.users holds 128,110 rows of a doubled-suffix
// form, near-inert but enough to make almost every bare name match twice.
func (t *target) BackfillUserIDs(ctx context.Context) (int64, error) {
	res, err := t.db.ExecContext(ctx, `
		UPDATE subjects s
		   SET user_id = u.id
		  FROM public.users u
		 WHERE s.subject_type = 'user'
		   AND s.user_id IS NULL
		   AND u.username = s.subject_id || $1
		   AND NOT EXISTS (SELECT 1 FROM subjects o WHERE o.user_id = u.id)`, t.suffix)
	if err != nil {
		return 0, fmt.Errorf("could not correlate subjects with DE users: %w", err)
	}
	return res.RowsAffected()
}

// CorrelationCounts returns how many user subjects are correlated with a DE user
// and how many are not. The uncorrelated ones are members who never became DE
// users -- a data-quality signal, never a gate.
//
// Reported alongside the backfill count because most subjects are correlated
// when they are created, not by the backfill: the backfill only picks up rows
// that already existed, so its count alone reads as far lower than the truth.
func (t *target) CorrelationCounts(ctx context.Context) (correlated, uncorrelated int, err error) {
	err = t.db.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE user_id IS NOT NULL),
		       count(*) FILTER (WHERE user_id IS NULL)
		  FROM subjects WHERE subject_type = 'user'`).Scan(&correlated, &uncorrelated)
	if err != nil {
		return 0, 0, fmt.Errorf("could not count subject correlation: %w", err)
	}
	return correlated, uncorrelated, nil
}

// GrantCounts returns the number of permissions held on each group resource, so
// a collision can be judged on grants as well as membership.
func (t *target) GrantCounts(ctx context.Context) (map[string]int, error) {
	rows, err := t.db.QueryContext(ctx, `
		SELECT r.name, count(p.id)
		  FROM resources r
		  JOIN resource_types rt ON rt.id = r.resource_type_id
		  LEFT JOIN permissions p ON p.resource_id = r.id
		 WHERE rt.name = 'group'
		 GROUP BY r.name`)
	if err != nil {
		return nil, fmt.Errorf("could not count group permissions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	counts := map[string]int{}
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			return nil, fmt.Errorf("could not read a group permission count: %w", err)
		}
		counts[name] = n
	}
	return counts, rows.Err()
}

// EffectiveMembership returns the computed closure as a set of
// "<group external id>\x00<username>" keys, for comparison against Grouper.
func (t *target) EffectiveMembership(ctx context.Context) (map[string]bool, error) {
	rows, err := t.db.QueryContext(ctx, `
		SELECT gs.subject_id, ms.subject_id
		  FROM group_effective_members em
		  JOIN subjects gs ON gs.id = em.group_id
		  JOIN subjects ms ON ms.id = em.member_id`)
	if err != nil {
		return nil, fmt.Errorf("could not read effective membership: %w", err)
	}
	defer func() { _ = rows.Close() }()

	set := map[string]bool{}
	for rows.Next() {
		var groupID, username string
		if err := rows.Scan(&groupID, &username); err != nil {
			return nil, fmt.Errorf("could not read an effective membership row: %w", err)
		}
		set[groupID+"\x00"+username] = true
	}
	return set, rows.Err()
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// SetMembersPublic records which groups expose their member list, replacing the
// whole set rather than adding to it: a group whose `readers` privilege was
// revoked in Grouper between runs has to revert, or the importer converges to
// the union of every run it has ever made.
//
// Only imported groups are touched. A group created natively since cutover has
// no legacy_name and its visibility is not Grouper's to decide.
func (t *target) SetMembersPublic(ctx context.Context, grouperIDs []string) (int64, error) {
	res, err := t.db.ExecContext(ctx, `
		UPDATE groups g
		   SET members_public = (s.subject_id = ANY($1))
		  FROM subjects s
		 WHERE s.id = g.subject_id
		   AND g.legacy_name IS NOT NULL
		   AND g.members_public IS DISTINCT FROM (s.subject_id = ANY($1))`,
		pq.Array(grouperIDs))
	if err != nil {
		return 0, fmt.Errorf("could not record which groups expose their members: %w", err)
	}
	return res.RowsAffected()
}
