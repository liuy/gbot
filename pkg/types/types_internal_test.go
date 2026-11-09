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

// Pin tests for types.go + wire.go mutation sweep.

func TestPinMessage_SetMetadataFromJSON_EmptyNoop(t *testing.T) {
	m := &Message{Model: "keep"}
	m.SetMetadataFromJSON("")
	if m.Model != "keep" || m.Usage != nil {
		t.Errorf("empty metadata mutated the message: %+v", m)
	}
	m.SetMetadataFromJSON(`{"model":"new","stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":4}}`)
	if m.Model != "new" || m.StopReason != "end_turn" || m.Usage == nil || m.Usage.InputTokens != 3 || m.Usage.OutputTokens != 4 {
		t.Errorf("metadata restore = model=%q stop=%q usage=%+v", m.Model, m.StopReason, m.Usage)
	}
}

func TestPinSkillCommand_AvailabilityAndNames(t *testing.T) {
	c := &SkillCommand{}
	if !c.MeetsAvailabilityRequirement() {
		t.Error("empty availability must pass")
	}
	gated := &SkillCommand{Availability: []string{"pro"}}
	if !gated.MeetsAvailabilityRequirement() {
		t.Error("gbot has no auth tiers: any availability must pass")
	}
	if got := (&SkillCommand{DisplayName: "x"}).UserFacingName(); got != "x" {
		t.Errorf("UserFacingName(DisplayName) = %q, want x", got)
	}
	if got := (&SkillCommand{Name: "n"}).UserFacingName(); got != "n" {
		t.Errorf("UserFacingName(Name only) = %q, want n", got)
	}
	if got := (&SkillCommand{DisplayName: "d", Name: "n"}).UserFacingName(); got != "d" {
		t.Errorf("UserFacingName(both) = %q, want d", got)
	}
	if got := (&SkillCommand{Name: "n", IsUserInvocable: true}).IsHidden(); got {
		t.Errorf("IsHidden(IsUserInvocable) = %v, want false", got)
	}
	if got := (&SkillCommand{Name: "n"}).IsHidden(); !got {
		t.Error("IsHidden(default) = false, want true")
	}
}
