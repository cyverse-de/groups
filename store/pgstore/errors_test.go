package pgstore

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/cyverse-de/groups/store"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
)

func TestTranslateErr(t *testing.T) {
	other := errors.New("connection reset")

	tests := []struct {
		name string
		in   error
		want error  // the sentinel the result must match, nil for pass-through
		msg  string // a fragment the message must contain
	}{
		{name: "nil stays nil"},
		{
			name: "no rows becomes not found",
			in:   sql.ErrNoRows,
			want: store.ErrNotFound,
		},
		{
			name: "identity collision names the fields that collided",
			in:   &pq.Error{Code: codeUniqueViolation, Constraint: "groups_identity_unique"},
			want: store.ErrConflict,
			msg:  "already exists",
		},
		{
			name: "any other unique violation is still a conflict",
			in:   &pq.Error{Code: codeUniqueViolation, Constraint: "subjects_user_id_unique"},
			want: store.ErrConflict,
		},
		{
			name: "a name holding a colon is bad input, not a conflict",
			in:   &pq.Error{Code: codeCheckViolation, Constraint: "groups_name_check1"},
			want: store.ErrInvalid,
			msg:  "':'",
		},
		{
			name: "a blank name is bad input",
			in:   &pq.Error{Code: codeCheckViolation, Constraint: "groups_name_check"},
			want: store.ErrInvalid,
			msg:  "name",
		},
		{
			name: "a blank owner is bad input",
			in:   &pq.Error{Code: codeCheckViolation, Constraint: "groups_owner_check"},
			want: store.ErrInvalid,
			msg:  "owner",
		},
		{
			name: "an unrecognized check violation is still bad input",
			in:   &pq.Error{Code: codeCheckViolation, Constraint: "groups_some_future_check"},
			want: store.ErrInvalid,
		},
		{
			name: "a missing owner reports the type mismatch",
			in:   &pq.Error{Code: codeForeignKeyViolation, Constraint: "groups_group_type_owner_present_fkey"},
			want: store.ErrOwnerRequired,
		},
		{
			name: "anything else passes through untouched",
			in:   other,
			want: other,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := translateErr(tt.in)

			if tt.want == nil {
				assert.NoError(t, got)
				return
			}
			assert.ErrorIs(t, got, tt.want)
			if tt.msg != "" {
				assert.Contains(t, got.Error(), tt.msg)
			}
		})
	}
}

// A driver's constraint names are an implementation detail of the schema.
// Echoing one into a response body tells a caller which table and index they
// tripped, and tells them nothing they can act on.
func TestTranslateErrDoesNotLeakConstraintNames(t *testing.T) {
	codes := []string{codeUniqueViolation, codeForeignKeyViolation, codeCheckViolation}
	constraints := []string{
		"groups_identity_unique",
		"groups_name_check",
		"groups_name_check1",
		"groups_owner_check",
		"subjects_user_id_unique",
		"group_memberships_pkey",
		"groups_group_type_owner_present_fkey",
	}

	for _, code := range codes {
		for _, constraint := range constraints {
			err := translateErr(&pq.Error{Code: pq.ErrorCode(code), Constraint: constraint})
			if err == nil {
				continue
			}
			assert.NotContains(t, err.Error(), constraint,
				"code %s constraint %s leaked its constraint name", code, constraint)
		}
	}
}
