// Package model holds the types the groups service exposes over HTTP. It has no
// dependency on any storage or directory backend, so the JSON contract is
// defined in exactly one place rather than by whichever package happens to back
// it.
package model

import "time"

// The kinds of group the DE models. These match the rows seeded into
// permissions.group_types.
const (
	GroupTypeCollaboratorList = "collaborator_list"
	GroupTypeTeam             = "team"
	GroupTypeCommunity        = "community"
	GroupTypeSystem           = "system"
)

// Member types, matching permissions.subject_type_enum.
const (
	MemberTypeUser  = "user"
	MemberTypeGroup = "group"
)

// Subject source identifiers. These are Grouper's values, kept because
// downstream consumers still compare against the exact strings.
const (
	SourceUser  = "ldap"
	SourceGroup = "g:gsa"
)

// Group is a group as callers see it. ID is the external identifier: a 32-hex
// string, preserved from Grouper for imported groups.
type Group struct {
	ID          string `json:"id"`
	GroupType   string `json:"group_type"`
	Owner       string `json:"owner,omitempty"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
	// MembersPublic reports whether a public group's member list is public too.
	// Grouper's `readers` privilege for GrouperAll as opposed to `viewers`; it
	// means nothing unless the group carries a read grant to GrouperAll.
	MembersPublic bool `json:"members_public"`
	// Joinable reports whether a user may add themselves without approval.
	// Grouper's `optins` for GrouperAll, as opposed to `readers`; the two
	// coincide in current data but mean different things.
	Joinable  bool      `json:"joinable"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Ref identifies a group by its structured identity rather than its ID. Owner is
// empty for the group types that are named globally.
type Ref struct {
	GroupType string
	Owner     string
	Name      string
}

// GroupSpec carries the fields needed to create a group.
type GroupSpec struct {
	GroupType     string
	Owner         string
	Name          string
	DisplayName   string
	Description   string
	MembersPublic bool
	Joinable      bool
}

// GroupUpdate carries the fields that may be changed after creation. Nil means
// "leave unchanged"; group type and owner are immutable and absent by design.
type GroupUpdate struct {
	Name          *string
	DisplayName   *string
	Description   *string
	MembersPublic *bool
	Joinable      *bool
}

// Subject is a user as callers see it, hydrated from the identity provider.
type Subject struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	FirstName   string `json:"first_name,omitempty"`
	LastName    string `json:"last_name,omitempty"`
	Email       string `json:"email,omitempty"`
	Institution string `json:"institution,omitempty"`
	SourceID    string `json:"source_id,omitempty"`
}

// MemberRef is a member of a group: either a user, identified by username, or
// another group, identified by its external ID.
type MemberRef struct {
	ID   string
	Type string
}

// MemberChange reports the outcome of one membership change. Err is nil on
// success; a failed change does not abort the rest of the batch.
type MemberChange struct {
	SubjectID string
	Type      string
	Err       error
}
