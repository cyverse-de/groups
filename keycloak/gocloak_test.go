package keycloak

import (
	"testing"

	"github.com/Nerzal/gocloak/v13"
	"github.com/stretchr/testify/assert"
)

func TestToGroup(t *testing.T) {
	tests := []struct {
		name string
		in   *gocloak.Group
		want Group
	}{
		{
			name: "nil group",
			in:   nil,
			want: Group{},
		},
		{
			name: "id and name only",
			in: &gocloak.Group{
				ID:   gocloak.StringP("abc-123"),
				Name: gocloak.StringP("my-group"),
			},
			want: Group{ID: "abc-123", Name: "my-group"},
		},
		{
			name: "with attributes",
			in: &gocloak.Group{
				ID:   gocloak.StringP("abc-123"),
				Name: gocloak.StringP("my-group"),
				Attributes: &map[string][]string{
					attrDescription:      {"a team"},
					attrDisplayExtension: {"My Group"},
				},
			},
			want: Group{
				ID:               "abc-123",
				Name:             "my-group",
				Description:      "a team",
				DisplayExtension: "My Group",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, toGroup(tt.in))
		})
	}
}

func TestToSubject(t *testing.T) {
	tests := []struct {
		name string
		in   *gocloak.User
		want Subject
	}{
		{
			name: "nil user",
			in:   nil,
			want: Subject{},
		},
		{
			name: "full user",
			in: &gocloak.User{
				ID:        gocloak.StringP("uuid-1"),
				Username:  gocloak.StringP("jdoe"),
				FirstName: gocloak.StringP("Jane"),
				LastName:  gocloak.StringP("Doe"),
				Email:     gocloak.StringP("jane@example.org"),
				Attributes: &map[string][]string{
					attrInstitution: {"CyVerse"},
				},
			},
			want: Subject{
				ID:          "jdoe",
				Name:        "jdoe",
				FirstName:   "Jane",
				LastName:    "Doe",
				Email:       "jane@example.org",
				Institution: "CyVerse",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, toSubject(tt.in))
		})
	}
}

func TestSpecAttributes(t *testing.T) {
	tests := []struct {
		name string
		spec GroupSpec
		want *map[string][]string
	}{
		{
			name: "empty spec yields nil",
			spec: GroupSpec{Name: "g"},
			want: nil,
		},
		{
			name: "description only",
			spec: GroupSpec{Name: "g", Description: "desc"},
			want: &map[string][]string{attrDescription: {"desc"}},
		},
		{
			name: "both attributes",
			spec: GroupSpec{Name: "g", Description: "desc", DisplayExtension: "Disp"},
			want: &map[string][]string{
				attrDescription:      {"desc"},
				attrDisplayExtension: {"Disp"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, specAttributes(tt.spec))
		})
	}
}

func TestFirstAttr(t *testing.T) {
	attrs := &map[string][]string{
		"k":     {"v1", "v2"},
		"empty": {},
	}

	assert.Equal(t, "v1", firstAttr(attrs, "k"))
	assert.Equal(t, "", firstAttr(attrs, "empty"))
	assert.Equal(t, "", firstAttr(attrs, "missing"))
	assert.Equal(t, "", firstAttr(nil, "k"))
}
