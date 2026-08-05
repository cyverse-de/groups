package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/cyverse-de/groups/store"
	"github.com/cyverse-de/groups/store/pgstore"
)

// logf writes a progress line. Reports go to stderr so that stdout stays free
// for anything a caller might want to pipe.
func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "grouper-import: "+format+"\n", args...)
}

// report accumulates what a run did, so that a no-op run is visibly a no-op.
// "Dry-run until boring" only works if boring is distinguishable from a run that
// crashed early.
type report struct {
	GroupsCreated   int
	GroupsUpdated   int
	GroupsUnchanged int

	MembersAdded   int
	MembersRemoved int

	GrantsAdded   int
	GrantsRemoved int

	// MembersPublicChanged counts groups whose member-list visibility moved,
	// which is Grouper's `readers` privilege for GrouperAll rather than a grant.
	MembersPublicChanged int64

	// JoinableChanged counts groups whose join-without-approval flag moved,
	// which is Grouper's `optins` privilege for GrouperAll.
	JoinableChanged int64

	NamesTrimmed              []string
	MembersTrimmed            []string
	GroupsGoneFromGrouper     []string
	GroupsCreatedNatively     []string
	GroupsSkippedForCollision []string
	MemberFailures            []string
	PrivilegesDropped         map[string]int

	UsersCorrelated      int64
	UsersCorrelatedTotal int
	UsersUncorrelated    int

	ClosureExpected int
	ClosureMissing  int
	ClosureExtra    int
}

func newReport() *report {
	return &report{PrivilegesDropped: map[string]int{}}
}

// Print writes the report. Every counter is printed even when zero: a run that
// reports nothing is indistinguishable from one that did nothing.
func (r *report) Print() {
	logf("groups: %d created, %d updated, %d unchanged",
		r.GroupsCreated, r.GroupsUpdated, r.GroupsUnchanged)
	logf("membership: %d added, %d removed", r.MembersAdded, r.MembersRemoved)
	logf("grants: %d added, %d removed", r.GrantsAdded, r.GrantsRemoved)
	logf("member-list visibility: %d groups changed", r.MembersPublicChanged)
	logf("join-without-approval: %d groups changed", r.JoinableChanged)
	logf("DE users: %d subjects correlated (%d by this backfill), %d uncorrelated",
		r.UsersCorrelatedTotal, r.UsersCorrelated, r.UsersUncorrelated)

	logf("effective membership vs Grouper: %d expected, %d missing, %d unexpected",
		r.ClosureExpected, r.ClosureMissing, r.ClosureExtra)

	printList("group names trimmed", r.NamesTrimmed)
	printList("member identifiers trimmed", r.MembersTrimmed)
	printList("groups in the database but no longer in Grouper (NOT deleted)", r.GroupsGoneFromGrouper)
	printList("groups created natively, never in Grouper (expected during the soak)", r.GroupsCreatedNatively)
	printList("groups skipped as empty duplicates of a colliding identity", r.GroupsSkippedForCollision)
	printList("members rejected", r.MemberFailures)

	if len(r.PrivilegesDropped) > 0 {
		reasons := sortedReasons(r.PrivilegesDropped)
		logf("privileges with no equivalent (%d kinds):", len(reasons))
		for _, reason := range reasons {
			logf("  %5d  %s", r.PrivilegesDropped[reason], reason)
		}
	}
}

// verificationFailure reports a run whose writes succeeded but whose outcome
// failed verification -- rejected members, or a closure that disagrees with
// Grouper's own expansion -- so the process exits nonzero instead of letting a
// wrong import pass for a boring one. Worded so it does not read as a crash:
// the data was written.
func (r *report) verificationFailure() error {
	if len(r.MemberFailures) == 0 && r.ClosureMissing == 0 && r.ClosureExtra == 0 {
		return nil
	}
	return fmt.Errorf("the import completed and data was written, but verification failed: "+
		"%d members rejected, %d effective memberships missing, %d unexpected; see the report above",
		len(r.MemberFailures), r.ClosureMissing, r.ClosureExtra)
}

func printList(label string, items []string) {
	if len(items) == 0 {
		logf("%s: none", label)
		return
	}
	logf("%s: %d", label, len(items))
	for _, item := range items {
		logf("  %s", item)
	}
}

// run performs the import.
func run(ctx context.Context, cfg *config) (*report, error) {
	rep := newReport()

	source, err := openGrouper(ctx, cfg.GrouperURI, cfg.Prefix)
	if err != nil {
		return nil, err
	}
	defer closeQuietly("grouper", source)

	tgt, err := openTarget(ctx, cfg.DatabaseURI, cfg.Schema, cfg.UserSuffix)
	if err != nil {
		return nil, err
	}
	defer closeQuietly("target", tgt)

	if err := tgt.RequireGrouperAuthoritative(ctx); err != nil {
		return nil, err
	}

	unlock, err := tgt.Lock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()

	groupStore, err := pgstore.Open(ctx, pgstore.Config{
		URI: cfg.DatabaseURI, Schema: cfg.Schema, UserSuffix: cfg.UserSuffix,
	})
	if err != nil {
		return nil, err
	}
	defer closeQuietly("group store", groupStore)

	parsedGroups, err := importGroups(ctx, cfg, source, tgt, groupStore, rep)
	if err != nil {
		return rep, err
	}

	if cfg.runsPermissions() {
		if err := importGrants(ctx, cfg, source, tgt, parsedGroups, rep); err != nil {
			return rep, err
		}
	}

	return rep, rep.verificationFailure()
}

// parsedGroup pairs a Grouper group with its resolved identity.
type parsedGroup struct {
	grouper grouperGroup
	parsed  *ParsedName
}

// importGroups reconciles groups and membership.
//
// Names are parsed first, for all groups, before anything is written: an
// unparsable name aborts the run, and aborting after a partial write would leave
// the operator guessing how far it got.
func importGroups(ctx context.Context, cfg *config, source *grouperSource, tgt *target,
	groupStore store.Store, rep *report) ([]parsedGroup, error) {

	groups, err := source.Groups(ctx)
	if err != nil {
		return nil, err
	}
	logf("read %d groups from Grouper under %q", len(groups), cfg.Prefix)

	parsedGroups := make([]parsedGroup, 0, len(groups))
	var unparsed []string
	for _, g := range groups {
		parsed, err := parseGroupName(cfg.Prefix, g.Name)
		if err != nil {
			if errors.Is(err, errUnparsedName) {
				unparsed = append(unparsed, g.Name)
				continue
			}
			return nil, err
		}
		if parsed.Trimmed {
			rep.NamesTrimmed = append(rep.NamesTrimmed, g.Name)
		}
		parsedGroups = append(parsedGroups, parsedGroup{grouper: g, parsed: parsed})
	}
	if len(unparsed) > 0 {
		return nil, fmt.Errorf("%d Grouper names did not parse and the import will not guess at them:\n  %s",
			len(unparsed), strings.Join(unparsed, "\n  "))
	}

	memberCounts, err := source.MemberCounts(ctx)
	if err != nil {
		return nil, err
	}
	grantCounts, err := tgt.GrantCounts(ctx)
	if err != nil {
		return nil, err
	}
	skipped, err := resolveCollisions(parsedGroups, func(pg parsedGroup) bool {
		return memberCounts[pg.grouper.Name] > 0 || grantCounts[pg.grouper.ID] > 0
	})
	if err != nil {
		return nil, err
	}
	if len(skipped) > 0 {
		kept := parsedGroups[:0]
		for _, pg := range parsedGroups {
			if skipped[pg.grouper.ID] {
				rep.GroupsSkippedForCollision = append(rep.GroupsSkippedForCollision,
					fmt.Sprintf("%s (%s)", pg.grouper.Name, pg.grouper.ID))
				continue
			}
			kept = append(kept, pg)
		}
		parsedGroups = kept
		sort.Strings(rep.GroupsSkippedForCollision)
	}

	existing, err := tgt.Groups(ctx)
	if err != nil {
		return nil, err
	}
	inGrouper := make(map[string]bool, len(parsedGroups))
	for _, pg := range parsedGroups {
		inGrouper[pg.grouper.ID] = true
	}
	rep.GroupsGoneFromGrouper, rep.GroupsCreatedNatively = classifyAbsentGroups(existing, inGrouper)

	if cfg.DryRun {
		logf("dry run: %d groups would be written", len(parsedGroups))
	}

	if !cfg.runsGroups() {
		return parsedGroups, nil
	}

	if err := writeGroups(ctx, cfg, tgt, parsedGroups, existing, rep); err != nil {
		return nil, err
	}

	return parsedGroups, importMembership(ctx, cfg, source, tgt, groupStore, parsedGroups, rep)
}

// writeGroups upserts every parsed group in one transaction, rolled back on a
// dry run.
func writeGroups(ctx context.Context, cfg *config, tgt *target, parsedGroups []parsedGroup,
	existing map[string]targetGroup, rep *report) error {

	tx, err := tgt.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("could not begin the group transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	for _, pg := range parsedGroups {
		before, seen := existing[pg.grouper.ID]
		if err := tgt.UpsertGroup(ctx, tx, pg.grouper.ID, pg.parsed, pg.grouper.Description); err != nil {
			return err
		}
		switch {
		case !seen:
			rep.GroupsCreated++
		case before.GroupType != pg.parsed.GroupType || before.Owner != pg.parsed.Owner ||
			before.Name != pg.parsed.Name:
			rep.GroupsUpdated++
		default:
			rep.GroupsUnchanged++
		}
	}

	if cfg.DryRun {
		return nil
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("could not commit the group transaction: %w", err)
	}
	committed = true
	return nil
}

// importMembership reconciles each group's direct membership to Grouper's.
//
// Membership goes through the groups service's own store rather than raw SQL, so
// that the identifier rules, the subject-to-DE-user correlation, and the
// effective-membership recomputation have exactly one implementation.
func importMembership(ctx context.Context, cfg *config, source *grouperSource, tgt *target,
	groupStore store.Store, parsedGroups []parsedGroup, rep *report) error {

	members, err := source.Members(ctx, grouperAdminUser)
	if err != nil {
		return err
	}
	logf("read %d direct memberships from Grouper", len(members))

	// Grouper names the group; the store wants its identifier.
	idByName := make(map[string]string, len(parsedGroups))
	for _, pg := range parsedGroups {
		idByName[pg.grouper.Name] = pg.grouper.ID
	}

	desired := map[string][]string{}
	for _, m := range members {
		groupID, ok := idByName[m.GroupName]
		if !ok {
			// A membership in a group that did not parse; the run has already
			// aborted if that happened, so this is a Grouper-side race.
			return fmt.Errorf("membership references group %q, which was not imported", m.GroupName)
		}

		subjectID := strings.TrimSpace(m.SubjectID)
		if subjectID != m.SubjectID {
			rep.MembersTrimmed = append(rep.MembersTrimmed,
				fmt.Sprintf("%q in %s", m.SubjectID, m.GroupName))
		}
		if subjectID == "" {
			rep.MemberFailures = append(rep.MemberFailures,
				fmt.Sprintf("blank member identifier in %s", m.GroupName))
			continue
		}
		desired[groupID] = append(desired[groupID], subjectID)
	}
	sort.Strings(rep.MembersTrimmed)

	if cfg.DryRun {
		logf("dry run: membership for %d groups would be reconciled", len(desired))
		return nil
	}

	for _, pg := range parsedGroups {
		groupID := pg.grouper.ID
		err := groupStore.WithTx(ctx, func(tx store.Tx) error {
			// Read the current membership first so the report can distinguish
			// additions from removals; ReplaceMembers reports the changes it
			// made without saying which direction each went.
			before, err := tx.ListMembers(ctx, groupID)
			if err != nil {
				return err
			}
			had := make(map[string]bool, len(before))
			for _, m := range before {
				had[m.ID] = true
			}
			want := make(map[string]bool, len(desired[groupID]))
			for _, id := range desired[groupID] {
				want[id] = true
			}
			for id := range want {
				if !had[id] {
					rep.MembersAdded++
				}
			}
			for id := range had {
				if !want[id] {
					rep.MembersRemoved++
				}
			}

			changes, err := tx.ReplaceMembers(ctx, groupID, desired[groupID], importActor)
			if err != nil {
				return err
			}
			for _, c := range changes {
				if c.Err != nil {
					rep.MemberFailures = append(rep.MemberFailures,
						fmt.Sprintf("%s in %s: %s", c.SubjectID, pg.grouper.Name, c.Err))
					// A rejected member was counted as an addition above.
					rep.MembersAdded--
				}
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("could not reconcile membership of %q: %w", pg.grouper.Name, err)
		}
	}

	backfilled, err := tgt.BackfillUserIDs(ctx)
	if err != nil {
		return err
	}
	rep.UsersCorrelated = backfilled

	correlated, uncorrelated, err := tgt.CorrelationCounts(ctx)
	if err != nil {
		return err
	}
	rep.UsersCorrelatedTotal = correlated
	rep.UsersUncorrelated = uncorrelated

	return verifyClosure(ctx, source, tgt, parsedGroups, rep)
}

// verifyClosure compares the effective membership computed on this side against
// Grouper's own expansion.
//
// This is the strongest check available on the closure logic: Grouper already
// materializes the transitive expansion, so the two must agree exactly, per
// group. A mismatch means the closure is wrong, which is an authorization bug in
// either direction.
func verifyClosure(ctx context.Context, source *grouperSource, tgt *target,
	parsedGroups []parsedGroup, rep *report) error {

	grouperEffective, err := source.EffectiveMembers(ctx, grouperAdminUser)
	if err != nil {
		return err
	}

	idByName := make(map[string]string, len(parsedGroups))
	imported := make(map[string]bool, len(parsedGroups))
	for _, pg := range parsedGroups {
		idByName[pg.grouper.Name] = pg.grouper.ID
		imported[pg.grouper.ID] = true
	}

	expected := map[string]bool{}
	for _, m := range grouperEffective {
		groupID, ok := idByName[m.GroupName]
		if !ok {
			// A group that was skipped as a colliding duplicate.
			continue
		}
		expected[groupID+"\x00"+strings.TrimSpace(m.SubjectID)] = true
	}

	actual, err := tgt.EffectiveMembership(ctx)
	if err != nil {
		return err
	}

	var missing, extra int
	for key := range expected {
		if !actual[key] {
			missing++
		}
	}
	for key := range actual {
		groupID, _, _ := strings.Cut(key, "\x00")
		if !imported[groupID] {
			continue
		}
		if !expected[key] {
			extra++
		}
	}

	rep.ClosureExpected = len(expected)
	rep.ClosureMissing = missing
	rep.ClosureExtra = extra
	return nil
}

func closeQuietly(what string, c interface{ Close() error }) {
	if err := c.Close(); err != nil {
		logf("could not close the %s connection: %s", what, err)
	}
}

// importActor is recorded as group_memberships.added_by for imported rows.
const importActor = "grouper-import"

// grouperAdminUser is Grouper's own service account. Its memberships and
// privileges are noise: it uses the admin.users bypass instead.
const grouperAdminUser = "de_grouper"
