package skills

import (
	"testing"
)

func TestRegisterBundledSkills_LoadsGoal(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	reg.RegisterBundledSkills()

	all := reg.GetAllSkills()
	var found bool
	for _, s := range all {
		if s.Name == "goal" {
			found = true
			if s.Description == "" {
				t.Error("goal skill has empty description")
			}
			if s.Content == "" {
				t.Error("goal skill has empty content")
			}
			if !s.IsUserInvocable {
				t.Error("goal skill should be user-invocable")
			}
		}
	}
	if !found {
		t.Fatal("goal skill not found in bundled skills")
	}
}

func TestRegisterBundledSkills_AvailableViaFindSkill(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	reg.RegisterBundledSkills()

	cmd := reg.FindSkill("goal")
	if cmd == nil {
		t.Fatal("FindSkill(\"goal\") returned nil")
	}
	if cmd.Name != "goal" {
		t.Errorf("Name = %q, want goal", cmd.Name)
	}
}
