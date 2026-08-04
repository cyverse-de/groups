package main

import (
	"fmt"
	"sort"
)

// classifyAbsentGroups splits the groups held in the database but not present in
// this Grouper read into two lists: the ones Grouper used to hold, and the ones
// created natively.
//
// The distinction matters throughout the soak. apps creates workshop-users
// lazily against whatever store it is pointed at, so during the migration a
// perfectly healthy deployment grows groups Grouper never had. Reporting those
// as having disappeared from Grouper sends an operator looking for a deletion
// that never happened, and buries a real one in the noise.
//
// legacy_name is the discriminator: the importer records the Grouper name on
// every group it writes, so a group without one was never imported.
func classifyAbsentGroups(existing map[string]targetGroup, inGrouper map[string]bool) (gone, native []string) {
	for id, g := range existing {
		if inGrouper[id] {
			continue
		}
		entry := fmt.Sprintf("%s (%s/%s)", id, g.GroupType, g.Name)
		if g.LegacyName == "" {
			native = append(native, entry)
			continue
		}
		gone = append(gone, entry)
	}
	sort.Strings(gone)
	sort.Strings(native)
	return gone, native
}
