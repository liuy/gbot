package lsptool

import (
	"context"
	"os/exec"
	"strings"

	"github.com/liuy/gbot/pkg/lsp"
	"github.com/liuy/gbot/pkg/utils/proc"
)

const gitIgnoreBatchSize = 50

// filterGitIgnored removes locations whose file paths are gitignored.
// Uses `git check-ignore` with batched path arguments for efficiency.
// Source: LSPTool.ts:552-612 — filterGitIgnoredLocations
func filterGitIgnored(ctx context.Context, locs []lsp.Location, cwd string) []lsp.Location {
	if len(locs) == 0 {
		return locs
	}

	uriToPath := make(map[string]string)
	for _, loc := range locs {
		if _, ok := uriToPath[loc.URI]; !ok {
			uriToPath[loc.URI] = lsp.URItoPath(loc.URI)
		}
	}

	uniquePaths := make([]string, 0, len(uriToPath))
	seen := make(map[string]bool)
	for _, p := range uriToPath {
		if !seen[p] {
			seen[p] = true
			uniquePaths = append(uniquePaths, p)
		}
	}

	ignored := checkGitIgnore(ctx, uniquePaths, cwd)
	if len(ignored) == 0 {
		return locs
	}

	filtered := locs[:0]
	for _, loc := range locs {
		p := uriToPath[loc.URI]
		if !ignored[p] {
			filtered = append(filtered, loc)
		}
	}
	return filtered
}

func checkGitIgnore(ctx context.Context, paths []string, cwd string) map[string]bool {
	ignored := make(map[string]bool)
	for i := 0; i < len(paths); i += gitIgnoreBatchSize {
		end := min(i+gitIgnoreBatchSize, len(paths))
		batch := paths[i:end]

		cmd := exec.CommandContext(ctx, "git", append([]string{"check-ignore"}, batch...)...)
		proc.HideWindow(cmd)
		cmd.Dir = cwd
		output, err := cmd.Output()
		if err != nil {
			// exit code 1 = nothing ignored, 128 = not a git repo
			continue
		}
		for line := range strings.SplitSeq(string(output), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				ignored[line] = true
			}
		}
	}
	return ignored
}
