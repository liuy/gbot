package types

// Compile-time interface compliance checks.
var _ PermissionResult = PermissionAllowDecision{}
var _ PermissionResult = PermissionAskDecision{}
var _ PermissionResult = PermissionDenyDecision{}
