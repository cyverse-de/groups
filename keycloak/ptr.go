package keycloak

// Bool returns a pointer to the given value. It is a convenience for
// populating optional *bool fields on option structs and representations.
func Bool(b bool) *bool { return &b }

// Int returns a pointer to the given value. It is a convenience for
// populating optional *int fields on option structs.
func Int(i int) *int { return &i }
