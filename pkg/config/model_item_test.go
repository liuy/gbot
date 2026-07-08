package config

import "testing"

func TestBuildModelItems_RegularThenFree(t *testing.T) {
	zhipu := &Provider{Name: "zhipu"}
	zhipu.Models.Set("glm-5.2", ModelConfig{})
	zhipu.Models.Set("glm-4.6", ModelConfig{})

	openai := &Provider{Name: "openai"}
	openai.Models.Set("gpt-5", ModelConfig{})

	free := &Provider{Name: "openrouter-free", Free: true}
	free.Models.Set("llama-free", ModelConfig{})

	configs := map[string]*Provider{
		"zhipu":           zhipu,
		"openai":          openai,
		"openrouter-free": free,
	}
	present := func(name string) bool { return true }

	items := BuildModelItems(configs, present, "zhipu", "glm-5.2")

	if len(items) != 4 {
		t.Fatalf("items length = %d, want 4", len(items))
	}
	// Regular alphabetical: openai, zhipu. Then free: openrouter-free.
	wantOrder := []struct{ Provider, Model string }{
		{"openai", "gpt-5"},
		{"zhipu", "glm-5.2"},
		{"zhipu", "glm-4.6"},
		{"openrouter-free", "llama-free"},
	}
	for i, w := range wantOrder {
		if items[i].Provider != w.Provider || items[i].Model != w.Model {
			t.Errorf("items[%d] = %s/%s, want %s/%s", i, items[i].Provider, items[i].Model, w.Provider, w.Model)
		}
	}
	// Current flag on zhipu/glm-5.2.
	if !items[1].Current {
		t.Error("items[1] (zhipu/glm-5.2) should be Current")
	}
	for i, item := range items {
		if i != 1 && item.Current {
			t.Errorf("items[%d] should not be Current", i)
		}
	}
}

func TestBuildModelItems_SkipsAbsentProviders(t *testing.T) {
	zhipu := &Provider{Name: "zhipu"}
	zhipu.Models.Set("glm-5.2", ModelConfig{})

	noKey := &Provider{Name: "no-key"}
	noKey.Models.Set("missing", ModelConfig{})

	configs := map[string]*Provider{
		"zhipu":  zhipu,
		"no-key": noKey,
	}
	present := func(name string) bool { return name != "no-key" }

	items := BuildModelItems(configs, present, "", "")
	if len(items) != 1 {
		t.Fatalf("items length = %d, want 1 (no-key skipped)", len(items))
	}
	if items[0].Provider != "zhipu" || items[0].Model != "glm-5.2" {
		t.Errorf("items[0] = %s/%s, want zhipu/glm-5.2", items[0].Provider, items[0].Model)
	}
}

func TestBuildModelItems_Empty(t *testing.T) {
	items := BuildModelItems(nil, func(string) bool { return true }, "", "")
	if len(items) != 0 {
		t.Fatalf("items length = %d, want 0", len(items))
	}
}

func TestBuildModelItems_NoCurrentMatch(t *testing.T) {
	zhipu := &Provider{Name: "zhipu"}
	zhipu.Models.Set("glm-5.2", ModelConfig{})

	configs := map[string]*Provider{"zhipu": zhipu}
	items := BuildModelItems(configs, func(string) bool { return true }, "other", "other-model")
	if items[0].Current {
		t.Error("item should not be Current when provider/model don't match")
	}
}
