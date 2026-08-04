package main

import "sort"

// communityIndex is every community the permissions schema knows, keyed the
// three ways a stored tag might name one.
type communityIndex struct {
	// byID holds the community IDs themselves, so a value already migrated is
	// recognised and left alone.
	byID map[string]bool
	// byLegacyName maps the Grouper name a community was imported under to its
	// ID. This is what almost every existing tag holds.
	byLegacyName map[string]string
	// byName maps the short name, for tags written against a deployment that
	// never had Grouper.
	byName map[string]string
}

// classifyTagValues splits the distinct stored tag values into the ones that can
// be rewritten to a community ID and the ones that name no community at all.
//
// Orphans are reported rather than repaired: a value naming a community that no
// longer exists cannot be resolved to anything, and deleting the rows would
// discard the only remaining record of what an app was once tagged with.
func classifyTagValues(values []string, communities communityIndex) (map[string]string, []string) {
	rewrite := map[string]string{}
	var orphan []string

	for _, value := range values {
		if communities.byID[value] {
			continue
		}
		// The legacy Grouper name is checked before the short name: a community
		// whose short name happened to equal another's full path would otherwise
		// capture that one's tags.
		if id, ok := communities.byLegacyName[value]; ok {
			rewrite[value] = id
			continue
		}
		if id, ok := communities.byName[value]; ok {
			rewrite[value] = id
			continue
		}
		orphan = append(orphan, value)
	}

	sort.Strings(orphan)
	return rewrite, orphan
}
