package main

import (
	"context"
	"fmt"
	"sort"

	"github.com/cyverse-de/groups/permissions"
)

// resourceTypeGroup is the permissions resource type each group is registered
// under, matching the groups service.
const resourceTypeGroup = "group"

// importGrants reconciles the permissions held on each group.
//
// Grants go through the permissions HTTP API rather than SQL because that
// service owns resource and subject creation. The calls are idempotent by verb
// -- PUT to grant, DELETE to revoke -- so a re-run after a partial failure is
// slow rather than wrong, and no progress journal is needed.
func importGrants(ctx context.Context, cfg *config, source *grouperSource, tgt *target,
	parsedGroups []parsedGroup, rep *report) error {

	client := permissions.NewClient(cfg.Permissions)
	if err := client.EnsureResourceType(ctx, resourceTypeGroup,
		"A CyVerse Discovery Environment group."); err != nil {
		return fmt.Errorf("could not register the %q resource type: %w", resourceTypeGroup, err)
	}

	privileges, err := source.Privileges(ctx)
	if err != nil {
		return err
	}
	logf("read %d privileges from Grouper", len(privileges))

	idByName := make(map[string]string, len(parsedGroups))
	for _, pg := range parsedGroups {
		idByName[pg.grouper.Name] = pg.grouper.ID
	}

	// Desired grants per group, deduplicated: the three shapes of the public
	// marker collapse onto one read grant for GrouperAll.
	desired := map[string]map[grantSpec]bool{}
	for _, pg := range parsedGroups {
		desired[pg.grouper.ID] = map[grantSpec]bool{}
		if g := ownerGrant(pg.parsed); g != nil {
			desired[pg.grouper.ID][*g] = true
		}
	}

	// Groups whose member list Grouper exposed. Collected alongside the grants
	// because it comes from the same privilege rows, but recorded on the group
	// rather than as a grant -- see membersPublicPrivilege.
	membersPublic := map[string]bool{}
	// Joinability is a separate privilege from member visibility; see
	// joinablePrivilege for why it is not derived from the other.
	joinable := map[string]bool{}

	for _, p := range privileges {
		groupID, ok := idByName[p.GroupName]
		if !ok {
			// The group was skipped as a colliding duplicate, so its privileges
			// go nowhere; counted so the drop is visible in the report.
			rep.PrivilegesDropped["held on a group that was not imported"]++
			continue
		}
		if membersPublicPrivilege(p.ListName, p.SubjectID) {
			membersPublic[groupID] = true
		}
		if joinablePrivilege(p.ListName, p.SubjectID) {
			joinable[groupID] = true
		}
		grant, reason := classifyPrivilege(p.ListName, p.SubjectID, p.Source, grouperAdminUser)
		if grant == nil {
			rep.PrivilegesDropped[reason]++
			continue
		}
		desired[groupID][*grant] = true
	}

	// One level per subject, before anything is sent.
	for groupID, set := range desired {
		desired[groupID] = collapseGrants(set)
	}

	if cfg.DryRun {
		total := 0
		for _, set := range desired {
			total += len(set)
		}
		logf("dry run: %d grants would be applied across %d groups", total, len(desired))
		logf("dry run: %d groups would expose their member list", len(membersPublic))
		logf("dry run: %d groups would be joinable without approval", len(joinable))
		return nil
	}

	publicIDs := make([]string, 0, len(membersPublic))
	for id := range membersPublic {
		publicIDs = append(publicIDs, id)
	}
	changed, err := tgt.SetMembersPublic(ctx, publicIDs)
	if err != nil {
		return err
	}
	rep.MembersPublicChanged = changed

	joinableIDs := make([]string, 0, len(joinable))
	for id := range joinable {
		joinableIDs = append(joinableIDs, id)
	}
	joinChanged, err := tgt.SetJoinable(ctx, joinableIDs)
	if err != nil {
		return err
	}
	rep.JoinableChanged = joinChanged

	for _, pg := range parsedGroups {
		if err := applyGrants(ctx, client, pg, desired[pg.grouper.ID], rep); err != nil {
			return err
		}
	}
	return nil
}

// applyGrants reconciles one group's permissions: grant what is missing, revoke
// what Grouper no longer justifies.
func applyGrants(ctx context.Context, client permissions.Client, pg parsedGroup,
	want map[grantSpec]bool, rep *report) error {

	current, err := client.ListResource(ctx, resourceTypeGroup, pg.grouper.ID)
	if err != nil {
		return fmt.Errorf("could not list permissions on %q: %w", pg.grouper.Name, err)
	}

	have := map[grantSpec]bool{}
	for _, p := range current {
		have[grantSpec{
			SubjectType: p.Subject.SubjectType,
			SubjectID:   p.Subject.SubjectID,
			Level:       p.PermissionLevel,
		}] = true
	}

	for spec := range want {
		if have[spec] {
			continue
		}
		if err := client.Grant(ctx, resourceTypeGroup, pg.grouper.ID,
			spec.SubjectType, spec.SubjectID, spec.Level); err != nil {
			return fmt.Errorf("could not grant %s on %q to %s: %w",
				spec.Level, pg.grouper.Name, spec.SubjectID, err)
		}
		rep.GrantsAdded++
	}

	for spec := range have {
		if want[spec] {
			continue
		}
		// Only revoke a subject we would otherwise manage: a level changed in
		// Grouper shows up as a different spec for the same subject, and the
		// grant above has already replaced it.
		if wantsSubject(want, spec) {
			continue
		}
		if err := client.Revoke(ctx, resourceTypeGroup, pg.grouper.ID,
			spec.SubjectType, spec.SubjectID); err != nil {
			return fmt.Errorf("could not revoke %s on %q from %s: %w",
				spec.Level, pg.grouper.Name, spec.SubjectID, err)
		}
		rep.GrantsRemoved++
	}
	return nil
}

// wantsSubject reports whether the desired set grants this subject anything,
// at any level.
func wantsSubject(want map[grantSpec]bool, spec grantSpec) bool {
	for w := range want {
		if w.SubjectType == spec.SubjectType && w.SubjectID == spec.SubjectID {
			return true
		}
	}
	return false
}

// sortedReasons orders the dropped-privilege reasons for a stable report.
func sortedReasons(m map[string]int) []string {
	reasons := make([]string, 0, len(m))
	for reason := range m {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	return reasons
}
