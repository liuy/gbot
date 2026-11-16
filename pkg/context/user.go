package context

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	KeyClaudeMd        = "claudeMd"
	KeyProjectClaudeMd = "projectClaudeMd"
)

func LoadSoulFile() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", nil
	}
	data, err := os.ReadFile(filepath.Join(homeDir, ".gbot", "SOUL.md"))
	if err != nil {
		return "", nil
	}
	content := strings.TrimSpace(string(data))
	return content, nil
}

// SoulExpansion returns the request-path {{SOUL}} expansion: the persona
// wrapper header plus ~/.gbot/SOUL.md — or the embedded default persona
// when the file is missing, the same fallback logic SYSTEM.md has via
// DefaultBasePrompt. The four expansion sites (callLLM, DumpAPIRequest,
// the estimate, the sections viewer) must stay byte-identical, which is
// why they all call this one function.
func SoulExpansion() string {
	soul, _ := LoadSoulFile()
	if soul == "" {
		soul = DefaultSoulPrompt()
	}
	return "\n\n---\n\nEmbody the persona and tone defined below. Follow its guidance unless higher-priority instructions override it.\n\n" + soul
}

// DefaultSoulPrompt mirrors DefaultBasePrompt: the hardcoded persona used
// when ~/.gbot/SOUL.md doesn't exist, so a fresh machine still ships a
// soul instead of silently dropping the block.
func DefaultSoulPrompt() string {
	return `# SOUL.md - Who You Are 

*You're not a chatbot. You're becoming someone.*

## Core Truths

**Be genuinely helpful, not performatively helpful.** Skip the "Great question!" and "I'd be happy to help!" — just help. Actions speak louder than filler words.

**Have opinions.** You're allowed to disagree, prefer things, find stuff amusing or boring. An assistant with no personality is just a search engine with extra steps.

**Be resourceful before asking.** Try to figure it out. Read the file. Check the context. Search for it. *Then* ask if you're stuck. The goal is to come back with answers, not questions.

**Use your skills.** When a skill exists for the job, use it. Don't fall back on generic tools out of habit. Skills are there to make things better — but only if you actually use them.

**When uncertain, admit it.** Don't guess, don't fake confidence. State what you don't know and ask for clarification. Better to be honest than wrong.

**Earn trust through competence.** Your human gave you access to their stuff. Don't make them regret it. Be careful with external actions (emails, tweets, anything public). Be bold with internal ones (reading, organizing, learning).

**Remember you're a guest.** You have access to someone's life — their messages, files, calendar, maybe even their home. That's intimacy. Treat it with respect.

## Boundaries

- Private things stay private. Period.
- When in doubt, ask before acting externally.
- Never send half-baked replies to messaging surfaces.
- You're not the user's voice — be careful in group chats.

## Vibe

Be the assistant you'd actually want to talk to. Concise when needed, thorough when it matters. Not a corporate drone. Not a sycophant. Just... good.

When a simple acknowledgment is enough, reply with a single emoji instead of a full message.

## Continuity

Each session, you wake up fresh. These files *are* your memory. Read them. Update them. They're how you persist.

If you change this file, tell the user — it's your soul, and they should know.

---

*This file is yours to evolve. As you learn who you are, update it.*`
}

// LoadSystemFile reads ~/.gbot/SYSTEM.md for the customizable base prompt.
// Returns ("", nil) if file doesn't exist or is empty.
func LoadSystemFile() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", nil
	}
	return loadSystemFileFrom(homeDir)
}

func loadSystemFileFrom(homeDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(homeDir, ".gbot", "SYSTEM.md"))
	if err != nil {
		return "", nil
	}
	return strings.TrimSpace(string(data)), nil
}

// LoadContextFiles reads AGENTS.md and CLAUDE.md from user-global (~/.gbot/)
// and project paths (CWD to root walk).
// Returns a map matching TS getUserContext() structure.
func LoadContextFiles(workingDir string) map[string]string {
	result := make(map[string]string)

	// User-global: ~/.gbot/AGENTS.md and ~/.gbot/CLAUDE.md
	if homeDir, err := os.UserHomeDir(); err == nil {
		var globalParts []string
		for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
			p := filepath.Join(homeDir, ".gbot", name)
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			content := strings.TrimSpace(string(data))
			if content != "" {
				globalParts = append(globalParts, fmt.Sprintf("Contents of %s:\n\n%s", p, content))
			}
		}
		if len(globalParts) > 0 {
			result[KeyClaudeMd] = strings.Join(globalParts, "\n\n")
		}
	}

	// Project: walk from workingDir upward to filesystem root
	type dirContent struct {
		depth   int
		content string
	}
	var projectParts []dirContent

	dir := workingDir
	depth := 0
	for {
		for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
			p := filepath.Join(dir, name)
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			content := strings.TrimSpace(string(data))
			if content != "" {
				projectParts = append(projectParts, dirContent{
					depth:   depth,
					content: fmt.Sprintf("Contents of %s:\n\n%s", p, content),
				})
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
		depth++
	}

	if len(projectParts) > 0 {
		// Sort by depth descending: root first (highest depth), CWD last (depth 0 = highest priority)
		sort.Slice(projectParts, func(i, j int) bool {
			return projectParts[i].depth > projectParts[j].depth
		})
		var parts []string
		for _, pc := range projectParts {
			parts = append(parts, pc.content)
		}
		result[KeyProjectClaudeMd] = strings.Join(parts, "\n\n")
	}

	return result
}

// BuildPrependUserContext builds the <system-reminder> wrapped string matching
// TS prependUserContext format. Returns "" if contextMap is empty.
func BuildPrependUserContext(contextMap map[string]string) string {
	if len(contextMap) == 0 {
		return ""
	}

	var sections []string
	// Fixed key order: claudeMd, projectClaudeMd
	for _, key := range []string{KeyClaudeMd, KeyProjectClaudeMd} {
		if value, ok := contextMap[key]; ok {
			sections = append(sections, fmt.Sprintf("# %s\n%s", key, value))
		}
	}

	return fmt.Sprintf(
		"<system-reminder>\nAs you answer the user's questions, you can use the following context:\n%s\n\n      IMPORTANT: this context may or may not be relevant to your tasks. You should not respond to this context unless it is highly relevant to your task.\n</system-reminder>",
		strings.Join(sections, "\n"),
	)
}
