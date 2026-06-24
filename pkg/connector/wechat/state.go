package wechat

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/liuy/gbot/pkg/utils"
)

// StatePath returns ~/.gbot/wechat/state.json
func StatePath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, ".gbot", "wechat", "state.json"), nil
}

// LoadState reads state.json. Returns nil if file does not exist.
func LoadState() (*State, error) {
	path, err := StatePath()
	if err != nil {
		return nil, err
	}
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

// SaveState writes state atomically. Creates parent directory if needed.
func SaveState(s *State) error {
	path, err := StatePath()
	if err != nil {
		return err
	}
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
