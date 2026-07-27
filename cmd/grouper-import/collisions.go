package main

import (
	"fmt"
	"sort"
	"strings"
)

// identityKey is the group identity as groups_identity_unique compares it:
// case-insensitive, with runs of whitespace collapsed.
func identityKey(parsed *ParsedName) string {
	return strings.ToLower(parsed.GroupType) + "\x00" +
		strings.ToLower(parsed.Owner) + "\x00" +
		strings.ToLower(strings.Join(strings.Fields(parsed.Name), " "))
}

// resolveCollisions finds Grouper groups that would land on the same identity
// and decides which of each set to import.
//
// Grouper names are case-sensitive and the target index is not, so a pair like
// `Sriram Team` / `sriram team` cannot both exist here. Production has exactly
// one such pair and both sides are empty, so aborting on it would block the
// migration over nothing.
//
// The rule: if at most one member of a colliding set has content -- members in
// Grouper or permission grants already in the target -- import that one and skip
// the rest, reported loudly. If two or more do, abort: choosing which real group
// survives is a human decision.
//
// hasContent is injected so the decision can be tested without a database.
func resolveCollisions(groups []parsedGroup, hasContent func(parsedGroup) bool) (map[string]bool, error) {
	byIdentity := map[string][]parsedGroup{}
	for _, pg := range groups {
		key := identityKey(pg.parsed)
		byIdentity[key] = append(byIdentity[key], pg)
	}

	skipped := map[string]bool{}
	var unresolved []string

	keys := make([]string, 0, len(byIdentity))
	for key := range byIdentity {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		set := byIdentity[key]
		if len(set) < 2 {
			continue
		}

		var withContent []parsedGroup
		for _, pg := range set {
			if hasContent(pg) {
				withContent = append(withContent, pg)
			}
		}

		if len(withContent) > 1 {
			names := make([]string, 0, len(withContent))
			for _, pg := range withContent {
				names = append(names, fmt.Sprintf("%q", pg.grouper.Name))
			}
			sort.Strings(names)
			unresolved = append(unresolved, strings.Join(names, " and "))
			continue
		}

		// Sorting by Grouper identifier keeps the survivor stable across runs;
		// the order rows come back in is not guaranteed.
		survivor := set[0]
		if len(withContent) == 1 {
			survivor = withContent[0]
		} else {
			ordered := append([]parsedGroup(nil), set...)
			sort.Slice(ordered, func(i, j int) bool {
				return ordered[i].grouper.ID < ordered[j].grouper.ID
			})
			survivor = ordered[0]
		}

		for _, pg := range set {
			if pg.grouper.ID != survivor.grouper.ID {
				skipped[pg.grouper.ID] = true
			}
		}
	}

	if len(unresolved) > 0 {
		return nil, fmt.Errorf(
			"%d group identity collisions have content on more than one side and cannot be "+
				"resolved automatically; rename or delete one side in Grouper:\n  %s",
			len(unresolved), strings.Join(unresolved, "\n  "))
	}
	return skipped, nil
}
