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

func TestPermissionResultMarkers(t *testing.T) {
	var _ PermissionResult = PermissionAllowDecision{}
	var _ PermissionResult = PermissionAskDecision{}
	var _ PermissionResult = PermissionDenyDecision{}

	allow2 := PermissionAllowDecision{}
	ask2 := PermissionAskDecision{}
	deny2 := PermissionDenyDecision{}
	allow2.permissionResultMarker()
	ask2.permissionResultMarker()
	deny2.permissionResultMarker()

	allow := PermissionAllowDecision{}
	if allow.Behavior() != BehaviorAllow {
		t.Error("AllowDecision.Behavior should be BehaviorAllow")
	}
	ask := PermissionAskDecision{}
	if ask.Behavior() != BehaviorAsk {
		t.Error("AskDecision.Behavior should be BehaviorAsk")
	}
	deny := PermissionDenyDecision{}
	if deny.Behavior() != BehaviorDeny {
		t.Error("DenyDecision.Behavior should be BehaviorDeny")
	}
}
