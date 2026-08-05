package main

import (
	"fmt"

	"github.com/cyverse-de/groups/model"
)

// Permission levels in the permissions service. Grouper's privilege vocabulary
// is wider than what the DE actually uses, so most of it maps to nothing.
const (
	levelOwn   = "own"
	levelAdmin = "admin"
	levelRead  = "read"
)

// Subject types in the permissions schema.
const (
	subjectTypeUser  = "user"
	subjectTypeGroup = "group"
)

// grouperAllSubject is Grouper's "everyone" pseudo-subject. It is a group-typed
// subject with no group row behind it, and a grant to it is how a community or
// team is marked public -- terrain and Sonora both read it that way.
const grouperAllSubject = "GrouperAll"

// Grouper subject sources seen in production under the DE folder.
const (
	sourceLDAP     = "ldap"
	sourceGroup    = "g:gsa"
	sourceInternal = "g:isa"
)

// grantSpec is a permission grant to make on a group resource.
type grantSpec struct {
	SubjectType string
	SubjectID   string
	Level       string
}

// classifyPrivilege maps one Grouper privilege to a permission grant, returning
// nil and a reason when the privilege has no equivalent. Callers report the
// reasons rather than discarding them, so that a privilege we failed to
// anticipate is visible instead of silently lost.
//
// Callers must pass only immediate privileges. Effective ones are Grouper's own
// expansion of nested groups, and importing them would duplicate the expansion
// the permissions service performs through group_effective_members.
func classifyPrivilege(listName, subjectID, subjectSource, grouperAdminUser string) (*grantSpec, string) {
	switch subjectSource {
	case sourceLDAP, sourceGroup, sourceInternal:
	default:
		return nil, fmt.Sprintf("unknown subject source %q", subjectSource)
	}

	// The public marker. Every g:isa row in production is GrouperAll, and it
	// arrives as readers, optins, or viewers depending on the group type.
	if subjectID == grouperAllSubject {
		switch listName {
		case "readers", "optins", "viewers":
			return &grantSpec{SubjectType: subjectTypeGroup, SubjectID: grouperAllSubject, Level: levelRead}, ""
		}
		return nil, fmt.Sprintf("%q held by %s has no equivalent", listName, grouperAllSubject)
	}

	subjectType := subjectTypeUser
	if subjectSource == sourceGroup {
		subjectType = subjectTypeGroup
	}

	switch listName {
	case "admins":
		if subjectID == grouperAdminUser {
			return nil, fmt.Sprintf("admins held by %q, which uses the admin.users bypass", grouperAdminUser)
		}
		return &grantSpec{SubjectType: subjectType, SubjectID: subjectID, Level: levelAdmin}, ""

	case "readers":
		return &grantSpec{SubjectType: subjectType, SubjectID: subjectID, Level: levelRead}, ""

	case "optins":
		// Membership already implies the ability to leave a group.
		return nil, "optins: membership already implies the ability to leave"

	case "optouts":
		// Redundant with membership.
		return nil, "optouts: redundant with membership"
	}

	return nil, fmt.Sprintf("unrecognized privilege %q", listName)
}

// ownerGrant synthesizes the `own` grant for a group's namespace owner. Grouper
// expressed ownership as folder structure rather than as a privilege, so without
// this an owner could not pass the `own` check that deleting a group requires.
//
// Communities and system groups have no owner; they are administered through
// `admins` grants.
func ownerGrant(parsed *ParsedName) *grantSpec {
	if parsed.Owner == "" {
		return nil
	}
	switch parsed.GroupType {
	case model.GroupTypeCollaboratorList, model.GroupTypeTeam:
		return &grantSpec{SubjectType: subjectTypeUser, SubjectID: parsed.Owner, Level: levelOwn}
	}
	return nil
}

// levelRank orders permission levels so that a subject holding more than one
// keeps the strongest. Only the levels this importer produces are ranked.
var levelRank = map[string]int{levelRead: 1, levelAdmin: 2, levelOwn: 3}

// collapseGrants reduces a group's desired grants to one level per subject.
//
// A collaborator-list owner typically holds `admins` in Grouper as well, which
// would produce two grants for one subject. The permissions API is keyed by
// (resource, subject), so the second would silently overwrite the first, and
// which one survived would depend on map iteration order -- leaving two runs of
// a supposedly convergent import with different results.
func collapseGrants(want map[grantSpec]bool) map[grantSpec]bool {
	strongest := map[string]grantSpec{}
	for spec := range want {
		key := spec.SubjectType + "\x00" + spec.SubjectID
		if current, seen := strongest[key]; seen && levelRank[current.Level] >= levelRank[spec.Level] {
			continue
		}
		strongest[key] = spec
	}

	collapsed := make(map[grantSpec]bool, len(strongest))
	for _, spec := range strongest {
		collapsed[spec] = true
	}
	return collapsed
}

// membersPublicPrivilege reports whether a privilege makes the group's member
// list public. Grouper's vocabulary separates `view` (the group exists and can
// be joined) from `read` (its membership can be listed), and applies the first
// to public teams and the second to public communities. Both import as the same
// GrouperAll read grant because the permissions service has no level weaker
// than read, so the difference is recorded on the group instead -- otherwise
// every public team's membership becomes world-readable. `optins` marks a group
// joinable and says nothing about who may list it.
func membersPublicPrivilege(listName, subjectID string) bool {
	return subjectID == grouperAllSubject && listName == "readers"
}

// joinablePrivilege reports whether a privilege lets any user add themselves to
// the group without approval. Grouper's `optins`, which the DE granted to public
// communities and withheld from public teams -- those go through the
// join-request flow, where an administrator approves.
//
// Deliberately not derived from membersPublicPrivilege: `readers` and `optins`
// coincide in current DE data but are different privileges, and treating one as
// the other silently makes every public team joinable.
func joinablePrivilege(listName, subjectID string) bool {
	return subjectID == grouperAllSubject && listName == "optins"
}
