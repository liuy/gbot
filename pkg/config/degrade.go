package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/liuy/gbot/pkg/llm"
)

// Degraded describes why a boot (or reload) could not honor the configured
// defaults and had to fall back. nil means fully healthy.
type Degraded struct {
	Reason string
}

// BootResult is the outcome of the boot degrade ladder: everything Start
// needs to wire an engine, plus the degradation trail.
type BootResult struct {
	Providers ProviderMap  // live providers; empty map = tier 2
	Provider  llm.Provider // NullProvider = tier 2
	Model     string       // "" = tier 2
	Primary   *Provider    // provider config backing Provider; zero value = tier 2
	Degraded  *Degraded    // nil = healthy or tier-1 fallback only
	Notes     []string     // tier-1 WARN lines (logs + reload report share them)
}

// ResolveBoot runs the degrade ladder over cfg. It never fails: every broken
// configuration lands on a deterministic fallback instead of an error, so
// startup can never die on config (user ruling: boot must always succeed).
// Judgment is purely mechanical — provider-map membership, never error-text
// matching, because ResolveModel's message shifts with fuzzy-match hits.
func ResolveBoot(cfg *Config) BootResult {
	refreshFreeModels(cfg)
	pm := buildProviderMap(cfg, "", "")
	if len(pm) == 0 {
		return BootResult{
			Providers: ProviderMap{},
			Provider:  llm.NewNullProvider(),
			Primary:   &Provider{},
			Degraded:  &Degraded{Reason: "no providers could be created (check API keys and models)"},
		}
	}

	p, modelName, err := cfg.ResolveModel()
	// The provider the default actually lands on: the resolved provider when
	// resolution succeeded, else the configured provider name. For bare model
	// defaults that resolve, this is the fuzzy-matched provider — the ladder
	// then correctly sees it as healthy.
	provName := ""
	if err == nil {
		provName = p.Name
	} else {
		provName, _, _ = cfg.ParseModel()
	}

	if _, alive := pm[provName]; alive && err == nil {
		// Healthy. Rebuild so the winning provider embeds the resolved model
		// (fidelity of the original CreateAllProviders loop).
		pm = buildProviderMap(cfg, provName, modelName)
		return BootResult{Providers: pm, Provider: pm[provName], Model: modelName, Primary: p}
	}

	if _, alive := pm[provName]; alive {
		// Tier 1a: provider alive, but the default model name matched nothing
		// in it (fuzzy fallback included). Stay on the user's provider choice,
		// fall back to its first declared model.
		fbCfg := cfg.findProvider(provName)
		fbModel := fbCfg.FirstModelName()
		note := fmt.Sprintf("default model %q not found in provider %q, falling back to first model %q",
			cfg.Model.String(), provName, fbModel)
		pm = buildProviderMap(cfg, provName, fbModel)
		return BootResult{
			Providers: pm,
			Provider:  pm[provName],
			Model:     fbModel,
			Primary:   fbCfg,
			Notes:     []string{note},
		}
	}

	// Tier 1b: the default provider is not in the live map (skipped for a
	// broken key, missing models, audio type, or simply not declared) or the
	// bare default matched no provider at all. Fall back to the first
	// provider alive in the map, in settings.json declaration order.
	for i := range cfg.Providers {
		cand := &cfg.Providers[i]
		if _, ok := pm[cand.Name]; !ok {
			continue
		}
		fbModel := cand.FirstModelName()
		note := fmt.Sprintf("default model %q has no live provider (provider %q not available), falling back to %s/%s",
			cfg.Model.String(), provName, cand.Name, fbModel)
		if provName == "" {
			note = fmt.Sprintf("default model %q not found in any provider, falling back to %s/%s",
				cfg.Model.String(), cand.Name, fbModel)
		}
		pm = buildProviderMap(cfg, cand.Name, fbModel)
		return BootResult{
			Providers: pm,
			Provider:  pm[cand.Name],
			Model:     fbModel,
			Primary:   cand,
			Notes:     []string{note},
		}
	}

	// Unreachable: pm is non-empty and every entry is backed by a cfg
	// provider. Kept defensive rather than panicking — boot must not die.
	return BootResult{
		Providers: ProviderMap{},
		Provider:  llm.NewNullProvider(),
		Primary:   &Provider{},
		Degraded:  &Degraded{Reason: "no providers could be created (check API keys and models)"},
	}
}

// LoadForgiving mirrors Load() but never fails: a settings.json that cannot
// be read or parsed is skipped and reported, good files still merge, and the
// returned cfg is never nil. Broken files are never rewritten — only user
// saves (Save*) ever write.
//
// Each file is decoded twice: once into a throwaway Config (validation), then
// the same bytes are merged into the accumulator. The two-pass shape exists
// because json.Unmarshal can write partial state before erroring (Models
// keeps entries parsed before a later failure — pinned by
// TestModels_UnmarshalJSON_InvalidValueAfterValid), and a shallow copy of the
// accumulator would share those containers with a file we must drop.
func LoadForgiving() (*Config, *Degraded) {
	cfg := DefaultConfig()
	var failures []string

	loadFile := func(path string) {
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				return
			}
			failures = append(failures, fmt.Sprintf("%s: %v", path, err))
			return
		}
		tmp := &Config{}
		if err := json.Unmarshal(data, tmp); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v%s", path, err, syntaxLineSuffix(data, err)))
			return
		}
		if err := json.Unmarshal(data, cfg); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v%s", path, err, syntaxLineSuffix(data, err)))
			return
		}
		// Files written before the rename to "desktops" still carry the old
		// remote_desktop key; without this fallback those console targets
		// would be silently dropped on load. Only the success path backfills.
		if cfg.Desktops == nil {
			var legacy struct {
				RemoteDesktop []RemoteDevice `json:"remote_desktop"`
			}
			if err := json.Unmarshal(data, &legacy); err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", path, err))
				return
			}
			cfg.Desktops = legacy.RemoteDesktop
		}
	}

	if homeDir, err := os.UserHomeDir(); err == nil {
		loadFile(filepath.Join(homeDir, ".gbot", "settings.json"))
	}
	loadFile(".gbot/settings.json")

	if v := os.Getenv("API_TIMEOUT_MS"); v != "" {
		var ms int
		if _, err := fmt.Sscanf(v, "%d", &ms); err == nil && ms > 0 {
			cfg.APITimeoutMS = ms
		}
	}

	if len(failures) == 0 {
		return cfg, nil
	}
	return cfg, &Degraded{Reason: "settings.json parse failed: " + strings.Join(failures, "; ")}
}

// syntaxLineSuffix converts a json.SyntaxError offset into a 1-based line
// number so the user can jump straight to the offending spot in the file.
func syntaxLineSuffix(data []byte, err error) string {
	var se *json.SyntaxError
	if !errors.As(err, &se) {
		return ""
	}
	if se.Offset <= 0 || se.Offset > int64(len(data)) {
		return ""
	}
	line := 1 + strings.Count(string(data[:se.Offset]), "\n")
	return fmt.Sprintf(" (line %d)", line)
}
