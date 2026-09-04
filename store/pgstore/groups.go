package pgstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/cyverse-de/groups/model"
	"github.com/cyverse-de/groups/store"
)

// GetGroup looks a group up by its external identifier.
func (r *reader) GetGroup(ctx context.Context, id string) (*model.Group, error) {
	query := `SELECT ` + groupColumns + `
		FROM groups g
		JOIN subjects s ON s.id = g.subject_id
		WHERE s.subject_id = $1`
	return scanGroup(r.q.QueryRowContext(ctx, query, id))
}

// LookupGroup looks a group up by its structured identity. The predicates mirror
// the groups_identity_unique index expression term for term so the index is
// used and so lookup agrees with what uniqueness allows.
func (r *reader) LookupGroup(ctx context.Context, ref model.Ref) (*model.Group, error) {
	query := `SELECT ` + groupColumns + `
		FROM groups g
		JOIN subjects s ON s.id = g.subject_id
		WHERE g.group_type = $1
		  AND lower(coalesce(g.owner, '')) = lower($2)
		  AND lower(trim(regexp_replace(g.name, '[[:space:]]+', ' ', 'g')))
		    = lower(trim(regexp_replace($3, '[[:space:]]+', ' ', 'g')))`
	return scanGroup(r.q.QueryRowContext(ctx, query, ref.GroupType, ref.Owner, ref.Name))
}

// ListGroups returns the groups matching the filter. A member filter restricts
// the results to groups the user belongs to through any depth of nesting.
func (r *reader) ListGroups(ctx context.Context, f store.ListFilter) ([]model.Group, error) {
	var (
		joins  strings.Builder
		where  []string
		args   []any
		nextID = func(v any) string {
			args = append(args, v)
			return fmt.Sprintf("$%d", len(args))
		}
	)

	if f.Member != "" {
		joins.WriteString(`
		JOIN group_effective_members em ON em.group_id = g.subject_id
		JOIN subjects ms ON ms.id = em.member_id AND ms.subject_id = ` + nextID(f.Member))
	}
	if f.GroupType != "" {
		where = append(where, "g.group_type = "+nextID(f.GroupType))
	}
	if f.Owner != "" {
		// Folded the way groups_identity_unique defines the owner, so this
		// agrees with LookupGroup: uniqueness already treats two spellings as
		// one owner, and an exact match would list nothing for a caller that
		// can still look the group up by name.
		where = append(where, "lower(coalesce(g.owner, '')) = lower("+nextID(f.Owner)+")")
	}
	if f.Search != "" {
		p := nextID("%" + escapeLike(f.Search) + "%")
		where = append(where, `(g.name ILIKE `+p+` ESCAPE '\' OR g.description ILIKE `+p+` ESCAPE '\')`)
	}
	if f.VisibleTo != "" {
		u := nextID(f.VisibleTo)
		// One EXISTS per way of being allowed to see a group, mirroring the
		// service's read check. The grant arm expands the user to their groups
		// the same way permission lookup does, because grants are held by groups
		// as well as users; GrouperAll is checked as itself, since it is a
		// sentinel nobody is a member of.
		where = append(where, `(
			EXISTS (SELECT 1
			          FROM group_effective_members vem
			          JOIN subjects vms ON vms.id = vem.member_id
			         WHERE vem.group_id = g.subject_id AND vms.subject_id = `+u+`)
			OR EXISTS (SELECT 1
			             FROM permissions vp
			             JOIN resources vr ON vr.id = vp.resource_id
			             JOIN resource_types vrt ON vrt.id = vr.resource_type_id
			            WHERE vrt.name = 'group'
			              AND vr.name = s.subject_id
			              AND (vp.subject_id IN (
			                       SELECT vu.id FROM subjects vu WHERE vu.subject_id = `+u+`
			                       UNION
			                       SELECT vgem.group_id
			                         FROM group_effective_members vgem
			                         JOIN subjects vgu ON vgu.id = vgem.member_id
			                        WHERE vgu.subject_id = `+u+`)
			                   OR vp.subject_id = (SELECT id FROM subjects WHERE subject_id = 'GrouperAll')))
		)`)
	}

	query := `SELECT ` + groupColumns + `
		FROM groups g
		JOIN subjects s ON s.id = g.subject_id` + joins.String()
	if len(where) > 0 {
		query += "\n\t\tWHERE " + strings.Join(where, "\n\t\t  AND ")
	}
	query += "\n\t\tORDER BY g.group_type, g.owner NULLS FIRST, g.name"

	if f.Limit > 0 {
		query += " LIMIT " + nextID(f.Limit)
	}
	if f.Offset > 0 {
		query += " OFFSET " + nextID(f.Offset)
	}

	rows, err := r.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, translateErr(err)
	}
	return scanGroups(rows)
}

// GroupsForSubject returns the groups a user belongs to, through any depth of
// nesting. Effective rather than direct membership, matching what Grouper
// reported and what permission lookup expands.
//
// It delegates to ListGroups rather than running its own query so the
// visibility rule cannot drift from the one on /groups: a second copy of that
// predicate is a second place for it to be wrong, and this endpoint leaked a
// user's whole membership when it had none.
func (r *reader) GroupsForSubject(ctx context.Context, username, groupType, visibleTo string) ([]model.Group, error) {
	return r.ListGroups(ctx, store.ListFilter{
		Member:    username,
		GroupType: groupType,
		VisibleTo: visibleTo,
	})
}

// CreateGroup inserts the group's subject row and the group itself. The subject
// identifier is left to the column default, which mints the 32-hex form that
// imported Grouper identifiers use.
func (t *tx) CreateGroup(ctx context.Context, spec model.GroupSpec) (*model.Group, error) {
	var internalID, externalID string
	err := t.tx.QueryRowContext(ctx,
		`INSERT INTO subjects (subject_type) VALUES ('group') RETURNING id, subject_id`,
	).Scan(&internalID, &externalID)
	if err != nil {
		return nil, translateErr(err)
	}

	_, err = t.tx.ExecContext(ctx,
		`INSERT INTO groups (subject_id, group_type, owner, name, display_name, description, members_public, joinable)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		internalID, spec.GroupType, nullString(spec.Owner), spec.Name,
		nullString(spec.DisplayName), spec.Description, spec.MembersPublic, spec.Joinable)
	if err != nil {
		return nil, translateErr(err)
	}

	return t.GetGroup(ctx, externalID)
}

// UpdateGroup changes a group's name, display name, and description. Group type
// and owner are immutable: the owner is the namespace segment the group's
// identity is built from, not a transferable ownership record.
func (t *tx) UpdateGroup(ctx context.Context, id string, upd model.GroupUpdate) (*model.Group, error) {
	var (
		sets []string
		args = []any{id}
		next = func(v any) string {
			args = append(args, v)
			return fmt.Sprintf("$%d", len(args))
		}
	)

	if upd.Name != nil {
		sets = append(sets, "name = "+next(*upd.Name))
	}
	if upd.DisplayName != nil {
		sets = append(sets, "display_name = "+next(nullString(*upd.DisplayName)))
	}
	if upd.Description != nil {
		sets = append(sets, "description = "+next(*upd.Description))
	}
	if upd.MembersPublic != nil {
		sets = append(sets, "members_public = "+next(*upd.MembersPublic))
	}
	if upd.Joinable != nil {
		sets = append(sets, "joinable = "+next(*upd.Joinable))
	}
	if len(sets) == 0 {
		return t.GetGroup(ctx, id)
	}

	res, err := t.tx.ExecContext(ctx,
		`UPDATE groups SET `+strings.Join(sets, ", ")+`
		 WHERE subject_id = (SELECT id FROM subjects WHERE subject_id = $1 AND subject_type = 'group')`,
		args...)
	if err != nil {
		return nil, translateErr(err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, translateErr(err)
	}
	if affected == 0 {
		return nil, store.ErrNotFound
	}

	return t.GetGroup(ctx, id)
}

// DeleteGroup removes the group by deleting its subject row, which cascades to
// the group, its membership, and any permission granted to it. The BEFORE DELETE
// trigger on groups detaches it from any group containing it and repairs their
// effective membership first. Deleting a group that does not exist succeeds, so
// the operation is idempotent.
func (t *tx) DeleteGroup(ctx context.Context, id string) error {
	_, err := t.tx.ExecContext(ctx,
		`DELETE FROM subjects WHERE subject_id = $1 AND subject_type = 'group'`, id)
	return translateErr(err)
}
