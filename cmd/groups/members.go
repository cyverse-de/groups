package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/cyverse-de/groups/model"
	"github.com/cyverse-de/groups/permissions"
	"github.com/cyverse-de/groups/store"
	"github.com/labstack/echo/v4"
)

// maxBulkMembers caps a single bulk membership request. Membership changes run
// in one transaction and recompute effective membership for every containing
// group, so an unbounded request would hold a write transaction open across the
// read path.
const maxBulkMembers = 1000

// membersResponse wraps a list of group members.
type membersResponse struct {
	Members []model.Subject `json:"members"`
	// Redacted marks a member list withheld because the group is public but
	// its membership is not. Without it an empty list is indistinguishable
	// from a genuinely empty group, and a service that reconciles state from
	// this endpoint -- group-propagator replaces iRODS ACLs from it -- treats
	// the redaction as truth and deletes every member.
	Redacted bool `json:"redacted,omitempty"`

	// Total is the group's full direct membership, which exceeds the members
	// returned when the caller asked for a page.
	Total int `json:"total"`
}

// membersRequest is the body for bulk membership operations.
type membersRequest struct {
	Members []string `json:"members"`
}

// memberResult reports the outcome of a single membership change.
type memberResult struct {
	SubjectID   string `json:"subject_id"`
	Success     bool   `json:"success"`
	Error       string `json:"error,omitempty"`
	SourceID    string `json:"source_id,omitempty"`
	SubjectName string `json:"subject_name,omitempty"`
}

// membersResults wraps the per-member outcomes of a bulk operation.
type membersResults struct {
	Results []memberResult `json:"results"`
}

// GetMembersHandler handles GET /groups/:id/members. Members may be other
// groups: those are reported with the group source ID, as Grouper did, so
// callers can tell them apart from users.
//
//	@Summary	List group members
//	@Produce	json
//	@Param	id	path	string	true	"Group identifier"
//	@Param	limit	query	int	false	"Maximum members to return"
//	@Param	offset	query	int	false	"Members to skip"
//	@Success	200	{object}	membersResponse
//	@Failure	404	{object}	map[string]string
//	@Failure	413	{object}	map[string]string	"The group has more members than one response may carry"
//	@Router	/groups/{id}/members [get]
func (a *App) GetMembersHandler(c echo.Context) error {
	groupID := c.Param("id")
	access, err := a.memberListAccess(c, groupID)
	if err != nil {
		return err
	}
	switch access {
	case membersDenied:
		return echo.NewHTTPError(http.StatusForbidden, "insufficient privileges")
	case membersRedacted:
		// Public, but its membership is not. Grouper answered this with an empty
		// list rather than an error, and the DE's team page depends on it.
		return c.JSON(http.StatusOK, &membersResponse{Members: []model.Subject{}, Redacted: true})
	}

	limit, err := intParam(c, "limit", 0)
	if err != nil {
		return err
	}
	offset, err := intParam(c, "offset", 0)
	if err != nil {
		return err
	}

	ctx := c.Request().Context()
	total, err := a.store.CountMembers(ctx, groupID)
	if err != nil {
		return storeError(err)
	}

	// Refusing beats truncating. group-propagator replaces an iRODS group's
	// membership from this response, so a page it mistook for the whole list
	// would delete everyone past the first page. A caller that wants the rest
	// has to ask for it.
	if limit == 0 && total > a.maxMemberListing {
		return echo.NewHTTPError(http.StatusRequestEntityTooLarge, fmt.Sprintf(
			"the group has %d members, more than the %d one response carries; page through it with limit and offset",
			total, a.maxMemberListing))
	}
	if limit == 0 || limit > a.maxMemberListing {
		limit = a.maxMemberListing
	}

	refs, err := a.store.ListMembers(ctx, groupID, store.MemberFilter{Limit: limit, Offset: offset})
	if err != nil {
		return storeError(err)
	}

	return c.JSON(http.StatusOK, &membersResponse{Members: a.hydrateMembers(ctx, refs), Total: total})
}

// hydrateMembers turns member references into subjects: users are looked up in
// the identity provider in one batch, and groups are described from the store.
func (a *App) hydrateMembers(ctx context.Context, refs []model.MemberRef) []model.Subject {
	usernames := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref.Type == model.MemberTypeUser {
			usernames = append(usernames, ref.ID)
		}
	}

	byUsername := make(map[string]model.Subject, len(usernames))
	if len(usernames) > 0 {
		// A directory failure must not fail the listing: membership lives in our
		// database, and the names are display data laid over it.
		subjects, err := a.userinfo.GetMany(ctx, usernames)
		if err != nil {
			log.WithField("context", "membership").
				Warnf("could not resolve member names; the membership is listed without them "+
					"(check portal-conductor.* settings and connectivity): %s", err)
		}
		for _, s := range subjects {
			byUsername[s.ID] = s
		}
	}

	members := make([]model.Subject, 0, len(refs))
	for _, ref := range refs {
		if ref.Type == model.MemberTypeGroup {
			members = append(members, a.groupSubject(ctx, ref.ID))
			continue
		}
		if s, ok := byUsername[ref.ID]; ok {
			members = append(members, s)
			continue
		}
		// The identity provider does not know this user. Membership is still
		// real -- it lives in our database -- so report the member rather than
		// dropping them.
		members = append(members, model.Subject{ID: ref.ID, Name: ref.ID, SourceID: model.SourceUser})
	}
	return members
}

// groupSubject describes a nested group as a subject, falling back to the bare
// identifier if it cannot be read.
func (a *App) groupSubject(ctx context.Context, groupID string) model.Subject {
	s := model.Subject{ID: groupID, Name: groupID, SourceID: model.SourceGroup}
	g, err := a.store.GetGroup(ctx, groupID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// A membership row pointing at a group with no groups row is a dangling
		// reference, which the schema is supposed to prevent.
		log.WithField("context", "membership").
			Warnf("nested group %s is a member but does not exist; its bare identifier is reported instead of a name", groupID)
	case err != nil:
		log.WithField("context", "membership").
			Warnf("could not resolve the name of nested group %s; its bare identifier is reported instead: %s", groupID, err)
	default:
		s.Name = g.Name
	}
	return s
}

// AddMembersHandler handles POST /groups/:id/members.
//
//	@Summary	Add members to a group
//	@Accept	json
//	@Produce	json
//	@Param	id	path	string	true	"Group identifier"
//	@Param	body	body	membersRequest	true	"Usernames or group identifiers to add"
//	@Success	200	{object}	membersResults
//	@Failure	502	{object}	map[string]string	"The identity provider could not be reached to verify new members"
//	@Router	/groups/{id}/members [post]
func (a *App) AddMembersHandler(c echo.Context) error {
	groupID := c.Param("id")
	if err := a.requireLevel(c, groupID, permissions.LevelAdmin); err != nil {
		return err
	}

	req, err := bindMembersRequest(c)
	if err != nil {
		return err
	}

	accepted, rejected, err := a.validateNewUsers(c.Request().Context(), req.Members)
	if err != nil {
		return err
	}

	user := actingUser(c)
	return a.applyMembership(c, groupID, rejected, func(ctx context.Context, tx store.Tx) ([]model.MemberChange, error) {
		return tx.AddMembers(ctx, groupID, accepted, user)
	})
}

// RemoveMembersHandler handles POST /groups/:id/members/deleter.
//
//	@Summary	Remove members from a group
//	@Accept	json
//	@Produce	json
//	@Param	id	path	string	true	"Group identifier"
//	@Param	body	body	membersRequest	true	"Usernames or group identifiers to remove"
//	@Success	200	{object}	membersResults
//	@Router	/groups/{id}/members/deleter [post]
func (a *App) RemoveMembersHandler(c echo.Context) error {
	groupID := c.Param("id")
	if err := a.requireLevel(c, groupID, permissions.LevelAdmin); err != nil {
		return err
	}

	req, err := bindMembersRequest(c)
	if err != nil {
		return err
	}

	// Removal is deliberately not validated: a user who has left the identity
	// provider must still be removable from a group.
	return a.applyMembership(c, groupID, nil, func(ctx context.Context, tx store.Tx) ([]model.MemberChange, error) {
		return tx.RemoveMembers(ctx, groupID, req.Members)
	})
}

// ReplaceMembersHandler handles PUT /groups/:id/members, making the membership
// exactly the supplied list and reporting only the changes it made.
//
//	@Summary	Replace all group members
//	@Accept	json
//	@Produce	json
//	@Param	id	path	string	true	"Group identifier"
//	@Param	body	body	membersRequest	true	"The complete desired membership"
//	@Success	200	{object}	membersResults
//	@Failure	404	{object}	map[string]string
//	@Failure	502	{object}	map[string]string	"The identity provider could not be reached to verify new members"
//	@Router	/groups/{id}/members [put]
func (a *App) ReplaceMembersHandler(c echo.Context) error {
	groupID := c.Param("id")
	if err := a.requireLevel(c, groupID, permissions.LevelAdmin); err != nil {
		return err
	}

	req, err := bindMembersRequest(c)
	if err != nil {
		return err
	}

	accepted, rejected, err := a.validateNewUsers(c.Request().Context(), req.Members)
	if err != nil {
		return err
	}

	user := actingUser(c)
	return a.applyMembership(c, groupID, rejected, func(ctx context.Context, tx store.Tx) ([]model.MemberChange, error) {
		return tx.ReplaceMembers(ctx, groupID, accepted, user)
	})
}

// AddMemberHandler handles PUT /groups/:id/members/:subject.
//
//	@Summary	Add a single member to a group
//	@Param	id	path	string	true	"Group identifier"
//	@Param	subject	path	string	true	"Username or group identifier"
//	@Success	200
//	@Failure	400	{object}	map[string]string	"No such user in the identity provider"
//	@Failure	404	{object}	map[string]string
//	@Failure	502	{object}	map[string]string	"The identity provider could not be reached"
//	@Router	/groups/{id}/members/{subject} [put]
func (a *App) AddMemberHandler(c echo.Context) error {
	groupID := c.Param("id")
	if err := a.requireLevel(c, groupID, permissions.LevelAdmin); err != nil {
		return err
	}

	subject := c.Param("subject")
	accepted, rejected, err := a.validateNewUsers(c.Request().Context(), []string{subject})
	if err != nil {
		return err
	}
	if len(rejected) > 0 {
		return storeError(rejected[0].Err)
	}

	user := actingUser(c)
	return a.applySingleMembership(c, groupID, subject,
		func(ctx context.Context, tx store.Tx) ([]model.MemberChange, error) {
			return tx.AddMembers(ctx, groupID, accepted, user)
		})
}

// RemoveMemberHandler handles DELETE /groups/:id/members/:subject. Removing a
// subject that is not a member succeeds.
//
//	@Summary	Remove a single member from a group
//	@Param	id	path	string	true	"Group identifier"
//	@Param	subject	path	string	true	"Username or group identifier"
//	@Success	200
//	@Failure	404	{object}	map[string]string
//	@Router	/groups/{id}/members/{subject} [delete]
func (a *App) RemoveMemberHandler(c echo.Context) error {
	groupID := c.Param("id")
	if err := a.requireLevel(c, groupID, permissions.LevelAdmin); err != nil {
		return err
	}

	subject := c.Param("subject")
	return a.applySingleMembership(c, groupID, subject,
		func(ctx context.Context, tx store.Tx) ([]model.MemberChange, error) {
			return tx.RemoveMembers(ctx, groupID, []string{subject})
		})
}

// errUnknownUser reports a username that the identity provider does not know.
var errUnknownUser = errors.New("no such user in the identity provider")

// validateNewUsers splits a requested membership into the members that may be
// written and the ones rejected because they would create a subject row for a
// username the identity provider does not know.
//
// Only identifiers without a subject row are checked. An identifier that already
// has one is either a group, or a user vetted when their row was created, or a
// user imported from Grouper who may since have left the directory -- none of
// which should be re-litigated by a membership change.
//
// A directory failure fails the request. Creating subject rows that cannot be
// verified is the outcome this validation exists to prevent, so it cannot fall
// back to allowing them.
func (a *App) validateNewUsers(ctx context.Context, ids []string) (accepted []string, rejected []model.MemberChange, err error) {
	existing, err := a.store.ExistingSubjects(ctx, ids)
	if err != nil {
		return nil, nil, storeError(err)
	}
	known := make(map[string]struct{}, len(existing))
	for _, id := range existing {
		known[id] = struct{}{}
	}

	unverified := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := known[id]; !ok {
			unverified = append(unverified, id)
		}
	}

	if len(unverified) > 0 {
		subjects, err := a.userinfo.GetMany(ctx, unverified)
		if err != nil {
			log.WithField("context", "membership").
				Errorf("could not verify new members against the identity provider; "+
					"refusing to create subject rows that cannot be verified: %s", err)
			return nil, nil, echo.NewHTTPError(http.StatusBadGateway,
				"could not verify the members against the identity provider")
		}
		for _, s := range subjects {
			known[s.ID] = struct{}{}
		}
	}

	accepted = make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := known[id]; ok {
			accepted = append(accepted, id)
			continue
		}
		rejected = append(rejected, model.MemberChange{SubjectID: id, Err: errUnknownUser})
	}
	return accepted, rejected, nil
}

// membershipOp is a membership change to run inside a transaction.
type membershipOp func(ctx context.Context, tx store.Tx) ([]model.MemberChange, error)

// applyMembership runs a bulk membership change and renders the per-member
// results, reporting members rejected before the transaction alongside the ones
// the store acted on. The change event is published after the commit, so a
// rolled-back transaction cannot announce a change that did not happen.
func (a *App) applyMembership(c echo.Context, groupID string, rejected []model.MemberChange, op membershipOp) error {
	ctx := c.Request().Context()

	var applied []model.MemberChange
	err := a.store.WithTx(ctx, func(tx store.Tx) error {
		var err error
		applied, err = op(ctx, tx)
		return err
	})
	if err != nil {
		return storeError(err)
	}

	a.publishGroupChanged(c, groupID)

	changes := make([]model.MemberChange, 0, len(rejected)+len(applied))
	changes = append(changes, rejected...)
	changes = append(changes, applied...)
	return c.JSON(http.StatusOK, &membersResults{Results: a.renderResults(ctx, changes)})
}

// applySingleMembership runs a change for one subject, reporting a rejected
// member as an error rather than a 200 with a failed result.
func (a *App) applySingleMembership(c echo.Context, groupID, subject string, op membershipOp) error {
	ctx := c.Request().Context()

	var changes []model.MemberChange
	err := a.store.WithTx(ctx, func(tx store.Tx) error {
		var err error
		changes, err = op(ctx, tx)
		return err
	})
	if err != nil {
		return storeError(err)
	}
	for _, change := range changes {
		if change.Err != nil {
			return storeError(change.Err)
		}
	}

	a.publishGroupChanged(c, groupID)
	return c.NoContent(http.StatusOK)
}

// renderResults converts store outcomes into the wire results, resolving the
// names and source IDs callers display. Users are resolved in one batch.
func (a *App) renderResults(ctx context.Context, changes []model.MemberChange) []memberResult {
	usernames := make([]string, 0, len(changes))
	for _, change := range changes {
		if change.Err == nil && change.Type == model.MemberTypeUser {
			usernames = append(usernames, change.SubjectID)
		}
	}

	byUsername := make(map[string]model.Subject, len(usernames))
	if len(usernames) > 0 {
		// A directory failure must not fail a membership change that already
		// committed; the names are decoration on a completed operation.
		subjects, err := a.userinfo.GetMany(ctx, usernames)
		if err != nil {
			log.WithField("context", "membership").
				Warnf("could not resolve member names; the change succeeded but names will be missing: %s", err)
		}
		for _, s := range subjects {
			byUsername[s.ID] = s
		}
	}

	results := make([]memberResult, 0, len(changes))
	for _, change := range changes {
		result := memberResult{SubjectID: change.SubjectID, Success: change.Err == nil}
		switch {
		case change.Err != nil:
			result.Error = change.Err.Error()
		case change.Type == model.MemberTypeGroup:
			result.SourceID = model.SourceGroup
			result.SubjectName = a.groupSubject(ctx, change.SubjectID).Name
		case change.Type == model.MemberTypeUser:
			result.SourceID = model.SourceUser
			result.SubjectName = change.SubjectID
			if s, ok := byUsername[change.SubjectID]; ok && s.Name != "" {
				result.SubjectName = s.Name
			}
		default:
			// Removing a subject that was not a member reports no type, so its
			// kind is unknown. Tagging it anyway would report an absent nested
			// group as a user, which is how consumers tell the two apart.
			result.SubjectName = change.SubjectID
		}
		results = append(results, result)
	}
	return results
}

// bindMembersRequest binds and validates a bulk membership body.
func bindMembersRequest(c echo.Context) (membersRequest, error) {
	var req membersRequest
	if err := c.Bind(&req); err != nil {
		return req, echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}
	if len(req.Members) > maxBulkMembers {
		return req, echo.NewHTTPError(http.StatusRequestEntityTooLarge,
			"too many members in one request")
	}
	return req, nil
}
