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

// TestLoad_PreservesBundledSkills verifies that Registry.Load() — which is
// the real entry point from main.go — does not wipe out the bundled skills
// registered via RegisterBundledSkills. Without the bundled skills merged
// into allSkills before the unconditional/conditional split, the final
// r.skills = unconditional assignment silently overwrites r.skills.
func TestLoad_PreservesBundledSkills(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(t.TempDir())
	if err := reg.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// /goal must be discoverable via FindSkill (the main lookup path).
	if cmd := reg.FindSkill("goal"); cmd == nil {
		t.Fatal("FindSkill(\"goal\") returned nil after Load() — bundled skill was wiped")
	}

	// And visible in GetAllSkills (the path used by GetSkillToolSkills →
	// RegisterSlashCommands in main.go).
	var found bool
	for _, s := range reg.GetAllSkills() {
		if s.Name == "goal" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("GetAllSkills() does not contain bundled \"goal\" skill after Load() — /goal will not be user-invocable")
	}
}
