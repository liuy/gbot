package main

import (
	"fmt"
	"os"
	"path/filepath"
	"github.com/user/gbot/pkg/config"
)

func main() {
	homeDir, _ := os.UserHomeDir()
	p := filepath.Join(homeDir, ".claude", "settings.minimax.json")
	cfg, err := config.LoadFromSettingsFile(p)
	if err != nil {
		fmt.Fprintf(os.Stderr, "err: %v\n", err)
		os.Exit(1)
	}
	key := cfg.APIKey
	fmt.Printf("key len=%d prefix=%q suffix=%q\n", len(key), key[:15], key[len(key)-10:])
	fmt.Printf("base_url=%q model=%q\n", cfg.BaseURL, cfg.Model)
}
