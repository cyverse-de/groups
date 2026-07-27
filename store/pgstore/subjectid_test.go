package pgstore

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateSubjectID covers the rules for an identifier that may become a
// subjects row. Surrounding whitespace matters because production Grouper holds
// member usernames with a trailing space (`cencas `, `m3gan `), and subject
// matching is exact -- such a row could never match the real user.
func TestValidateSubjectID(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{name: "an ordinary username", id: "alice"},
		{name: "a group identifier", id: "0b1c2d3e4f5061728394a5b6c7d8e9f0"},
		{name: "internal spaces are allowed", id: "de grouper"},
		{name: "empty", id: "", wantErr: true},
		{name: "only whitespace", id: "   ", wantErr: true},
		{name: "a trailing space", id: "cencas ", wantErr: true},
		{name: "a leading space", id: " alice", wantErr: true},
		{name: "a trailing tab", id: "alice\t", wantErr: true},
		{name: "a trailing newline", id: "alice\n", wantErr: true},
		{name: "longer than the column", id: strings.Repeat("a", maxSubjectIDLength+1), wantErr: true},
		{name: "exactly the column width", id: strings.Repeat("a", maxSubjectIDLength)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSubjectID(tt.id)
			if !tt.wantErr {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.True(t, isMemberError(err),
				"a bad identifier must be attributable to one member, not fail the batch")
		})
	}
}
