package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A run that wrote data but failed verification must exit nonzero, or "dry-run
// until boring" quietly ships a wrong closure. The error must also be
// distinguishable from a crash: the data was written.
func TestReportVerificationFailure(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*report)
		wantErr bool
	}{
		{name: "a clean run", mutate: func(*report) {}, wantErr: false},
		{
			name:    "rejected members",
			mutate:  func(r *report) { r.MemberFailures = []string{"ghost in a:team: invalid member"} },
			wantErr: true,
		},
		{
			name:    "effective membership missing",
			mutate:  func(r *report) { r.ClosureMissing = 1 },
			wantErr: true,
		},
		{
			name:    "unexpected effective membership",
			mutate:  func(r *report) { r.ClosureExtra = 2 },
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newReport()
			tt.mutate(r)

			err := r.verificationFailure()
			if !tt.wantErr {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "data was written",
				"the message must say the run wrote data, so it does not read as a crash")
		})
	}
}
