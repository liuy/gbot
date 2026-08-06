package dream

import (
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const watermarkFileName = ".dream-watermark"

// ReadWatermark returns the last consolidation timestamp from the watermark
// file. Returns a zero time.Time when the file is missing or corrupt —
// corrupt files are treated as "no consolidation yet" rather than a hard
// error, so a malformed watermark doesn't block dream permanently.
func ReadWatermark(memoryDir string) (time.Time, error) {
	path := filepath.Join(memoryDir, watermarkFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	ms, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return time.Time{}, nil
	}
	return time.UnixMilli(ms), nil
}

// WriteWatermark stamps the watermark file with the current time. Creates the
// parent directory if it doesn't exist.
func WriteWatermark(memoryDir string) error {
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(memoryDir, watermarkFileName)
	content := strconv.FormatInt(time.Now().UnixMilli(), 10)
	return os.WriteFile(path, []byte(content), 0o644)
}
