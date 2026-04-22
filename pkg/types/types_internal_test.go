package types

import "testing"

// Compile-time interface compliance checks.
var _ PermissionResult = PermissionAllowDecision{}
var _ PermissionResult = PermissionAskDecision{}
var _ PermissionResult = PermissionDenyDecision{}

func TestPermissionResultMarker(t *testing.T) {
	t.Parallel()

	// Call marker methods explicitly for coverage
	PermissionAllowDecision{}.permissionResultMarker()
	PermissionAskDecision{}.permissionResultMarker()
	PermissionDenyDecision{}.permissionResultMarker()
}
