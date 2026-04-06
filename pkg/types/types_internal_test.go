package types

import "testing"

func TestPermissionResultMarkerMethods(t *testing.T) {
	t.Parallel()

	PermissionAllowDecision{}.permissionResultMarker()
	PermissionAskDecision{}.permissionResultMarker()
	PermissionDenyDecision{}.permissionResultMarker()
}
