package pgstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/cyverse-de/groups/model"
	"github.com/cyverse-de/groups/store"
	"github.com/lib/pq"
)

// maxSubjectIDLength matches subjects.subject_id in the permissions schema.
// Checked before insert so an over-long name is reported against the member that
// caused it rather than failing the whole request.
const maxSubjectIDLength = 512

// ListMembers returns a group's direct members. A member may be another group:
// nesting is supported, and callers distinguish the two by Type.
func (r *reader) ListMembers(ctx context.Context, groupID string) ([]model.MemberRef, error) {
	if _, err := r.internalGroupID(ctx, groupID); err != nil {
		return nil, err
	}

	rows, err := r.q.QueryContext(ctx, `
		SELECT ms.subject_id, gm.member_type
		FROM group_memberships gm
		JOIN subjects gs ON gs.id = gm.group_id
		JOIN subjects ms ON ms.id = gm.member_id
		WHERE gs.subject_id = $1
		ORDER BY ms.subject_id`, groupID)
	if err != nil {
		return nil, translateErr(err)
	}
	// Close reports the same failure rows.Err() does, which is checked below.
	defer func() { _ = rows.Close() }()

	members := make([]model.MemberRef, 0)
	for rows.Next() {
		var m model.MemberRef
		if err := rows.Scan(&m.ID, &m.Type); err != nil {
			return nil, translateErr(err)
		}
		members = append(members, m)
	}
	return members, translateErr(rows.Err())
}

// IsEffectiveMember reports whether the user belongs to the group through any
// depth of nesting. This reads the materialized closure, so it is an index probe
// rather than a recursive walk.
func (r *reader) IsEffectiveMember(ctx context.Context, groupID, username string) (bool, error) {
	var found bool
	err := r.q.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM group_effective_members em
			JOIN subjects gs ON gs.id = em.group_id
			JOIN subjects ms ON ms.id = em.member_id
			WHERE gs.subject_id = $1 AND ms.subject_id = $2)`, groupID, username).Scan(&found)
	if err != nil {
		return false, translateErr(err)
	}
	return found, nil
}

// validateSubjectID checks an identifier that may become a subjects row.
//
// Surrounding whitespace is rejected rather than trimmed. Subject matching is
// exact, so `alice ` would become a second subject that no lookup could ever
// reach -- production Grouper holds four such member usernames. Trimming
// silently would instead make two different requests mean the same member,
// which is a worse surprise than an error.
func validateSubjectID(id string) error {
	switch {
	case id == "":
		return fmt.Errorf("%w: the subject identifier is empty", errMember)
	case strings.TrimSpace(id) != id:
		return fmt.Errorf("%w: the subject identifier has leading or trailing whitespace", errMember)
	case len(id) > maxSubjectIDLength:
		return fmt.Errorf("%w: the subject identifier is longer than %d characters",
			errMember, maxSubjectIDLength)
	}
	return nil
}

// ExistingSubjects returns the subset of ids that already have a subject row.
// The identifiers it does not return are the ones a membership change would mint
// a subject for, which is where user validation applies.
func (r *reader) ExistingSubjects(ctx context.Context, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	rows, err := r.q.QueryContext(ctx,
		`SELECT subject_id FROM subjects WHERE subject_id = ANY($1)`, pq.Array(ids))
	if err != nil {
		return nil, translateErr(err)
	}
	// Close reports the same failure rows.Err() does, which is checked below.
	defer func() { _ = rows.Close() }()

	existing := make([]string, 0, len(ids))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, translateErr(err)
		}
		existing = append(existing, id)
	}
	return existing, translateErr(rows.Err())
}

// internalGroupID resolves an external group identifier to the internal subject
// id the membership tables use, reporting ErrNotFound when there is no group.
func (r *reader) internalGroupID(ctx context.Context, groupID string) (string, error) {
	var id string
	err := r.q.QueryRowContext(ctx, `
		SELECT g.subject_id
		FROM groups g
		JOIN subjects s ON s.id = g.subject_id
		WHERE s.subject_id = $1`, groupID).Scan(&id)
	if err != nil {
		return "", translateErr(err)
	}
	return id, nil
}

// AddMembers adds each subject to the group.
//
// Members are validated before anything is written, because a failed statement
// aborts the surrounding transaction and would take the rest of the batch with
// it. Validation failures are reported per member, matching the bulk-operation
// contract; an unexpected database error fails the whole request.
func (t *tx) AddMembers(ctx context.Context, groupID string, subjectIDs []string, addedBy string) ([]model.MemberChange, error) {
	groupInternalID, err := t.internalGroupID(ctx, groupID)
	if err != nil {
		return nil, err
	}

	changes := make([]model.MemberChange, 0, len(subjectIDs))
	for _, subjectID := range subjectIDs {
		change := model.MemberChange{SubjectID: subjectID}

		memberInternalID, memberType, err := t.resolveMember(ctx, groupInternalID, subjectID)
		if err != nil {
			if isMemberError(err) {
				change.Err = err
				changes = append(changes, change)
				continue
			}
			return nil, err
		}
		change.Type = memberType

		_, err = t.tx.ExecContext(ctx, `
			INSERT INTO group_memberships (group_id, member_id, member_type, added_by)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (group_id, member_id) DO NOTHING`,
			groupInternalID, memberInternalID, memberType, nullString(addedBy))
		if err != nil {
			return nil, translateErr(err)
		}
		changes = append(changes, change)
	}

	if err := t.recomputeClosure(ctx, groupInternalID); err != nil {
		return nil, err
	}
	return changes, nil
}

// RemoveMembers removes each subject from the group. Removing a subject that is
// not a member succeeds, so the operation is idempotent.
func (t *tx) RemoveMembers(ctx context.Context, groupID string, subjectIDs []string) ([]model.MemberChange, error) {
	groupInternalID, err := t.internalGroupID(ctx, groupID)
	if err != nil {
		return nil, err
	}

	changes := make([]model.MemberChange, 0, len(subjectIDs))
	for _, subjectID := range subjectIDs {
		var memberType string
		err := t.tx.QueryRowContext(ctx, `
			DELETE FROM group_memberships gm
			USING subjects ms
			WHERE gm.member_id = ms.id
			  AND gm.group_id = $1
			  AND ms.subject_id = $2
			RETURNING gm.member_type`, groupInternalID, subjectID).Scan(&memberType)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			// Not a member. Report the change as applied: the caller's intent is
			// satisfied and DELETE is idempotent.
		case err != nil:
			return nil, translateErr(err)
		}
		changes = append(changes, model.MemberChange{SubjectID: subjectID, Type: memberType})
	}

	if err := t.recomputeClosure(ctx, groupInternalID); err != nil {
		return nil, err
	}
	return changes, nil
}

// ReplaceMembers makes the group's membership exactly the given set, reporting
// only the additions and removals it performed.
func (t *tx) ReplaceMembers(ctx context.Context, groupID string, subjectIDs []string, addedBy string) ([]model.MemberChange, error) {
	current, err := t.ListMembers(ctx, groupID)
	if err != nil {
		return nil, err
	}

	desired := make(map[string]bool, len(subjectIDs))
	for _, id := range subjectIDs {
		desired[id] = true
	}
	existing := make(map[string]bool, len(current))
	for _, m := range current {
		existing[m.ID] = true
	}

	var toRemove []string
	for _, m := range current {
		if !desired[m.ID] {
			toRemove = append(toRemove, m.ID)
		}
	}
	var toAdd []string
	for _, id := range subjectIDs {
		if !existing[id] {
			toAdd = append(toAdd, id)
		}
	}

	changes := make([]model.MemberChange, 0, len(toRemove)+len(toAdd))
	if len(toRemove) > 0 {
		removed, err := t.RemoveMembers(ctx, groupID, toRemove)
		if err != nil {
			return nil, err
		}
		changes = append(changes, removed...)
	}
	if len(toAdd) > 0 {
		added, err := t.AddMembers(ctx, groupID, toAdd, addedBy)
		if err != nil {
			return nil, err
		}
		changes = append(changes, added...)
	}
	return changes, nil
}

// resolveMember maps a subject identifier to the internal id and member type to
// store. An identifier naming an existing group becomes a nested group member;
// anything else is treated as a username and given a subject row if it has none.
func (t *tx) resolveMember(ctx context.Context, groupInternalID, subjectID string) (string, string, error) {
	if err := validateSubjectID(subjectID); err != nil {
		return "", "", err
	}

	var (
		internalID  string
		subjectType string
		isGroup     bool
	)
	err := t.q.QueryRowContext(ctx, `
		SELECT s.id, s.subject_type, (g.subject_id IS NOT NULL)
		FROM subjects s
		LEFT JOIN groups g ON g.subject_id = s.id
		WHERE s.subject_id = $1`, subjectID).Scan(&internalID, &subjectType, &isGroup)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		// An unknown identifier is a user we have not seen before. Membership
		// requires a subject row, so create one.
		return t.ensureUserSubject(ctx, subjectID)
	case err != nil:
		return "", "", translateErr(err)
	}

	if subjectType == model.MemberTypeGroup {
		if !isGroup {
			// A group-typed subject with no group row: GrouperAll, the sentinel
			// that marks a group public, is the one that exists in practice.
			return "", "", fmt.Errorf("%w: %q is not a group that can hold members", errMember, subjectID)
		}
		cycle, err := t.wouldCycle(ctx, groupInternalID, internalID)
		if err != nil {
			return "", "", err
		}
		if cycle {
			return "", "", fmt.Errorf("%w: %q already contains this group", store.ErrCycle, subjectID)
		}
		return internalID, model.MemberTypeGroup, nil
	}

	return internalID, model.MemberTypeUser, nil
}

// ensureUserSubject inserts a user subject row idempotently. DO UPDATE rather
// than DO NOTHING so RETURNING yields a row when another transaction inserted
// the same subject first; the assignment deliberately changes nothing.
func (t *tx) ensureUserSubject(ctx context.Context, subjectID string) (string, string, error) {
	var internalID, subjectType string
	err := t.tx.QueryRowContext(ctx, `
		INSERT INTO subjects (subject_id, subject_type) VALUES ($1, 'user')
		ON CONFLICT (subject_id) DO UPDATE SET subject_type = subjects.subject_type
		RETURNING id, subject_type`, subjectID).Scan(&internalID, &subjectType)
	if err != nil {
		return "", "", translateErr(err)
	}
	if subjectType != model.MemberTypeUser {
		return "", "", fmt.Errorf("%w: %q is not a user", errMember, subjectID)
	}
	return internalID, model.MemberTypeUser, nil
}

// wouldCycle reports whether adding member as a member of group would make a
// group contain itself. group_ancestors includes the group itself, so this also
// catches adding a group to itself.
func (t *tx) wouldCycle(ctx context.Context, groupInternalID, memberInternalID string) (bool, error) {
	var cycle bool
	err := t.q.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM group_ancestors(ARRAY[$1::uuid]) a WHERE a = $2::uuid)`,
		groupInternalID, memberInternalID).Scan(&cycle)
	if err != nil {
		return false, translateErr(err)
	}
	return cycle, nil
}

// recomputeClosure rebuilds the effective membership of the changed group and
// every group containing it. Called after every membership change; group
// deletion is handled by a trigger, which is the one path where the containing
// groups cannot be found afterwards.
func (t *tx) recomputeClosure(ctx context.Context, groupInternalID string) error {
	_, err := t.tx.ExecContext(ctx, `
		SELECT recompute_group_closure(
			ARRAY(SELECT a FROM group_ancestors(ARRAY[$1::uuid]) a))`, groupInternalID)
	return translateErr(err)
}

// errMember marks a failure attributable to one member of a bulk request rather
// than to the request as a whole.
var errMember = errors.New("invalid member")

// isMemberError reports whether an error should be attributed to a single member
// instead of failing the batch.
func isMemberError(err error) bool {
	return errors.Is(err, errMember) || errors.Is(err, store.ErrCycle)
}
