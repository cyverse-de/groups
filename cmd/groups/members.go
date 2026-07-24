package main

import (
	"context"
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
//	@Success	200	{object}	membersResponse
//	@Failure	404	{object}	map[string]string
//	@Router	/groups/{id}/members [get]
func (a *App) GetMembersHandler(c echo.Context) error {
	groupID := c.Param("id")
	if err := a.requireReadOrMember(c, groupID); err != nil {
		return err
	}

	ctx := c.Request().Context()
	refs, err := a.store.ListMembers(ctx, groupID)
	if err != nil {
		return storeError(err)
	}

	members, err := a.hydrateMembers(ctx, refs)
	if err != nil {
		return storeError(err)
	}
	return c.JSON(http.StatusOK, &membersResponse{Members: members})
}

// hydrateMembers turns member references into subjects: users are looked up in
// the identity provider in one batch, and groups are described from the store.
func (a *App) hydrateMembers(ctx context.Context, refs []model.MemberRef) ([]model.Subject, error) {
	usernames := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref.Type == model.MemberTypeUser {
			usernames = append(usernames, ref.ID)
		}
	}

	byUsername := make(map[string]model.Subject, len(usernames))
	if len(usernames) > 0 {
		subjects, err := a.userinfo.GetMany(ctx, usernames)
		if err != nil {
			return nil, err
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
	return members, nil
}

// groupSubject describes a nested group as a subject, falling back to the bare
// identifier if it cannot be read.
func (a *App) groupSubject(ctx context.Context, groupID string) model.Subject {
	s := model.Subject{ID: groupID, Name: groupID, SourceID: model.SourceGroup}
	g, err := a.store.GetGroup(ctx, groupID)
	if err == nil {
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
//	@Router	/groups/{id}/members [post]
func (a *App) AddMembersHandler(c echo.Context) error {
	groupID := c.Param("id")
	if err := a.requireLevel(c, groupID, permissions.LevelWrite); err != nil {
		return err
	}

	req, err := bindMembersRequest(c)
	if err != nil {
		return err
	}

	user := actingUser(c)
	return a.applyMembership(c, groupID, func(ctx context.Context, tx store.Tx) ([]model.MemberChange, error) {
		return tx.AddMembers(ctx, groupID, req.Members, user)
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
	if err := a.requireLevel(c, groupID, permissions.LevelWrite); err != nil {
		return err
	}

	req, err := bindMembersRequest(c)
	if err != nil {
		return err
	}

	return a.applyMembership(c, groupID, func(ctx context.Context, tx store.Tx) ([]model.MemberChange, error) {
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
//	@Router	/groups/{id}/members [put]
func (a *App) ReplaceMembersHandler(c echo.Context) error {
	groupID := c.Param("id")
	if err := a.requireLevel(c, groupID, permissions.LevelWrite); err != nil {
		return err
	}

	req, err := bindMembersRequest(c)
	if err != nil {
		return err
	}

	user := actingUser(c)
	return a.applyMembership(c, groupID, func(ctx context.Context, tx store.Tx) ([]model.MemberChange, error) {
		return tx.ReplaceMembers(ctx, groupID, req.Members, user)
	})
}

// AddMemberHandler handles PUT /groups/:id/members/:subject.
//
//	@Summary	Add a single member to a group
//	@Param	id	path	string	true	"Group identifier"
//	@Param	subject	path	string	true	"Username or group identifier"
//	@Success	200
//	@Failure	404	{object}	map[string]string
//	@Router	/groups/{id}/members/{subject} [put]
func (a *App) AddMemberHandler(c echo.Context) error {
	groupID := c.Param("id")
	if err := a.requireLevel(c, groupID, permissions.LevelWrite); err != nil {
		return err
	}

	subject := c.Param("subject")
	user := actingUser(c)
	return a.applySingleMembership(c, groupID, subject,
		func(ctx context.Context, tx store.Tx) ([]model.MemberChange, error) {
			return tx.AddMembers(ctx, groupID, []string{subject}, user)
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
	if err := a.requireLevel(c, groupID, permissions.LevelWrite); err != nil {
		return err
	}

	subject := c.Param("subject")
	return a.applySingleMembership(c, groupID, subject,
		func(ctx context.Context, tx store.Tx) ([]model.MemberChange, error) {
			return tx.RemoveMembers(ctx, groupID, []string{subject})
		})
}

// membershipOp is a membership change to run inside a transaction.
type membershipOp func(ctx context.Context, tx store.Tx) ([]model.MemberChange, error)

// applyMembership runs a bulk membership change and renders the per-member
// results. The change event is published after the commit, so a rolled-back
// transaction cannot announce a change that did not happen.
func (a *App) applyMembership(c echo.Context, groupID string, op membershipOp) error {
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

	a.publishGroupChanged(c, groupID)
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
		default:
			result.SourceID = model.SourceUser
			result.SubjectName = change.SubjectID
			if s, ok := byUsername[change.SubjectID]; ok && s.Name != "" {
				result.SubjectName = s.Name
			}
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
