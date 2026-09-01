package short

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/liuy/gbot/pkg/utils"
)

// EngineMeta is one entry in the engines array of WorkspaceMeta.
// Each engine in an EngineManager maps to one EngineMeta.
type EngineMeta struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	ActiveSessionID string `json:"active_session_id"`
	Model           string `json:"model"`
	Thinking        string `json:"thinking,omitempty"` // sticky runtime effort override
}

// WorkspaceMeta stores workspace-level session metadata in .gbot/meta.json.
type WorkspaceMeta struct {
	CurrentSessionID string       `json:"current_session_id,omitempty"`
	Engines          []EngineMeta `json:"engines,omitempty"`
	ActiveEngineID   string       `json:"active_engine_id,omitempty"`
	LastActiveAt     time.Time    `json:"last_active_at"`
}

// ReadWorkspaceMeta reads meta.json from the given project directory.
// Returns nil (no error) when the file doesn't exist.
// Returns an error for invalid JSON or other I/O errors.
func ReadWorkspaceMeta(projectDir string) (*WorkspaceMeta, error) {
	path := filepath.Join(projectDir, "meta.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read workspace meta: %w", err)
	}

	if len(data) == 0 {
		return nil, nil
	}

	var meta WorkspaceMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("parse workspace meta: %w", err)
	}

	return &meta, nil
}

// WriteWorkspaceMeta writes meta.json to the given project directory.
// Creates the project directory if it doesn't exist.
func WriteWorkspaceMeta(projectDir string, meta *WorkspaceMeta) error {
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		return fmt.Errorf("create project directory: %w", err)
	}

	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal workspace meta: %w", err)
	}

	path := filepath.Join(projectDir, "meta.json")
	if err := utils.AtomicWriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write workspace meta: %w", err)
	}
	return nil
}
