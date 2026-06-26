package wechat

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/liuy/gbot/pkg/utils"
)

// safeFilename replaces path separators and NUL in an accountID so it is a
// valid single-segment filename. iLink IDs look like "e1cc99a2c914@im.bot"
// (no separators), but this guards against malformed/malicious values.
func safeFilename(accountID string) string {
	name := strings.NewReplacer("/", "_", "\\", "_", "\x00", "_").Replace(accountID)
	if name == "" {
		return "account"
	}
	return name
}

// StateFilePath returns <projectDir>/wechat/<safeAccountID>.json.
func StateFilePath(projectDir, accountID string) string {
	return filepath.Join(projectDir, "wechat", safeFilename(accountID)+".json")
}

// LoadState loads one account's state. Returns nil if the file does not exist.
func LoadState(accountID, projectDir string) (*State, error) {
	path := StateFilePath(projectDir, accountID)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// LoadAllStates loads every <accountID>.json under <projectDir>/wechat/.
// Returns (nil, nil) when the directory does not exist or is empty.
// Skips files that fail to unmarshal (logs a warning with the filename) so one
// corrupt file does not block the others.
func LoadAllStates(projectDir string) ([]*State, error) {
	dir := filepath.Join(projectDir, "wechat")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var states []*State
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("wechat: skip unreadable state file", "file", entry.Name(), "error", err)
			continue
		}
		var s State
		if err := json.Unmarshal(data, &s); err != nil {
			slog.Warn("wechat: skip corrupt state file", "file", entry.Name(), "error", err)
			continue
		}
		states = append(states, &s)
	}
	return states, nil
}

// SaveState writes one account's state to
// <projectDir>/wechat/<safeAccountID>.json. Creates the wechat/ directory if
// needed.
func SaveState(s *State, projectDir string) error {
	path := StateFilePath(projectDir, s.AccountID)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return utils.AtomicWriteFile(path, data, 0644)
}
