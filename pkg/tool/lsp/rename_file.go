package lsptool

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/liuy/gbot/pkg/lsp"
	"github.com/liuy/gbot/pkg/tool"
)

// maxRenamePairs caps the directory-walk size for rename_file, mirroring omp
// MAX_RENAME_PAIRS (index.ts:405). A directory larger than this is rejected to
// keep LSP edits accurate.
const maxRenamePairs = 1000

type fileRenamePair struct {
	oldURI string
	newURI string
}

// enumerateRenamePairs walks source (file or directory) and produces one
// {oldURI, newURI} pair per regular file. Files: single pair. Directories:
// recursive walk anchored at dest, capped at maxRenamePairs.
// Mirrors omp enumerateRenamePairs (index.ts:417-451).
func enumerateRenamePairs(source, dest string) (pairs []fileRenamePair, dir bool, exceeded bool, err error) {
	info, err := os.Stat(source)
	if err != nil {
		return nil, false, false, err
	}
	if !info.IsDir() {
		return []fileRenamePair{{
			oldURI: lsp.FileToURI(source),
			newURI: lsp.FileToURI(dest),
		}}, false, false, nil
	}
	walkErr := filepath.WalkDir(source, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if len(pairs) >= maxRenamePairs {
			return errMaxRenamePairsReached
		}
		rel, rerr := filepath.Rel(source, p)
		if rerr != nil {
			return rerr
		}
		pairs = append(pairs, fileRenamePair{
			oldURI: lsp.FileToURI(p),
			newURI: lsp.FileToURI(filepath.Join(dest, rel)),
		})
		return nil
	})
	if walkErr != nil && walkErr != errMaxRenamePairsReached {
		return nil, false, false, walkErr
	}
	return pairs, true, walkErr == errMaxRenamePairsReached, nil
}

var errMaxRenamePairsReached = fmt.Errorf("max rename pairs reached")

// renameFile implements action=rename_file. Mirrors omp index.ts:1540-1810.
//
// Flow: validate params → enumerate pairs (file or dir walk, capped at 1000)
// → query workspace/willRenameFiles on each relevant server → coalesce edits
// per URI, discarding overlaps with conflict logging → apply edits to disk →
// os.Rename source→dest → notify workspace/didRenameFiles.
//
// Preview mode (apply=false): list the per-server edits that would be applied
// without touching the filesystem.
func renameFile(ctx context.Context, reg *lsp.Registry, in Input, workingDir string) (*tool.ToolResult, error) {
	source := strings.TrimSpace(in.File)
	dest := strings.TrimSpace(in.NewName)
	if source == "" || dest == "" {
		return nil, fmt.Errorf("rename_file requires both `file` (source path) and `new_name` (destination path)")
	}
	source = resolvePath(source, workingDir)
	dest = resolvePath(dest, workingDir)
	if source == dest {
		return nil, fmt.Errorf("source and destination paths are identical")
	}

	info, err := os.Stat(source)
	if err != nil {
		return nil, fmt.Errorf("source path does not exist: %s", lsp.URItoRelativePath(lsp.FileToURI(source), workingDir))
	}
	if _, err := os.Stat(dest); err == nil {
		return nil, fmt.Errorf("destination already exists: %s", lsp.URItoRelativePath(lsp.FileToURI(dest), workingDir))
	}

	pairs, _, exceeded, err := enumerateRenamePairs(source, dest)
	if err != nil {
		return nil, fmt.Errorf("enumerate rename pairs: %w", err)
	}
	if exceeded {
		return nil, fmt.Errorf("directory contains more than %d files; rename in smaller batches to keep LSP edits accurate", maxRenamePairs)
	}
	if len(pairs) == 0 {
		return nil, fmt.Errorf("no files to rename")
	}

	// Filter to relevant servers: a server is asked about the rename only if
	// it understands one of the affected file extensions.
	specs := reg.Snapshot()
	relevantSpecs := collectRelevantRenameServers(specs, source, dest, pairs)
	if len(relevantSpecs) == 0 {
		return renameFileNoServers(source, dest, pairs, info.IsDir(), workingDir)
	}

	type serverEdit struct {
		serverName string
		edit       *lsp.WorkspaceEdit
	}
	var perServer []serverEdit
	var serverNotes []string
	for _, spec := range relevantSpecs {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		c, err := reg.ForFile(ctx, source)
		if err != nil {
			serverNotes = append(serverNotes, fmt.Sprintf("  %s: %v", spec.Name, err))
			continue
		}
		params := map[string]any{"files": pairMaps(pairs)}
		raw, err := c.Request(ctx, "workspace/willRenameFiles", params)
		if err != nil {
			if isMethodNotFoundError(err) {
				continue
			}
			serverNotes = append(serverNotes, fmt.Sprintf("  %s: %v", spec.Name, err))
			continue
		}
		if len(raw) == 0 || string(raw) == "null" {
			continue
		}
		var edit lsp.WorkspaceEdit
		if err := json.Unmarshal(raw, &edit); err != nil {
			continue
		}
		if len(edit.Changes) == 0 && len(edit.DocumentChanges) == 0 {
			continue
		}
		perServer = append(perServer, serverEdit{spec.Name, &edit})
	}

	sourceLabel := lsp.URItoRelativePath(lsp.FileToURI(source), workingDir)
	destLabel := lsp.URItoRelativePath(lsp.FileToURI(dest), workingDir)
	fileCountLabel := sourceLabel
	if info.IsDir() {
		fileCountLabel = fmt.Sprintf("%d file%s under %s", len(pairs), pluralS(len(pairs)), sourceLabel)
	}

	apply := boolPtrVal(in.Apply, true)

	// PREVIEW MODE: no disk writes.
	if !apply {
		var b strings.Builder
		fmt.Fprintf(&b, "Rename preview: %s → %s\n", fileCountLabel, destLabel)
		if len(perServer) == 0 {
			b.WriteString("  No LSP edits would be applied\n")
		}
		for _, se := range perServer {
			edits := lsp.FormatWorkspaceEdit(se.edit, workingDir)
			if len(edits) == 0 {
				continue
			}
			fmt.Fprintf(&b, "  %s:\n", se.serverName)
			for _, e := range edits {
				fmt.Fprintf(&b, "    %s\n", e)
			}
		}
		if len(serverNotes) > 0 {
			b.WriteString("  Server notes:\n")
			for _, n := range serverNotes {
				fmt.Fprintf(&b, "%s\n", n)
			}
		}
		return &tool.ToolResult{Data: b.String()}, nil
	}

	// APPLY MODE: coalesce per-server edits by URI; first server wins on overlap,
	// later overlapping edits are discarded with conflict logging.
	type bucket struct {
		primaryServer   string
		edits           []lsp.TextEdit
		discarded       int
		conflictServers map[string]bool
	}
	acceptedByURI := make(map[string]*bucket)
	for _, se := range perServer {
		flat := lsp.FlattenWorkspaceTextEdits(se.edit)
		for uri, edits := range flat {
			existing := acceptedByURI[uri]
			if existing == nil {
				acceptedByURI[uri] = &bucket{
					primaryServer:   se.serverName,
					edits:           append([]lsp.TextEdit(nil), edits...),
					conflictServers: make(map[string]bool),
				}
				continue
			}
			var keptNew []lsp.TextEdit
			for _, ne := range edits {
				overlaps := false
				for _, ae := range existing.edits {
					if lsp.RangesOverlap(ae.Range, ne.Range) {
						overlaps = true
						break
					}
				}
				if overlaps {
					existing.discarded++
					existing.conflictServers[se.serverName] = true
				} else {
					keptNew = append(keptNew, ne)
				}
			}
			existing.edits = append(existing.edits, keptNew...)
		}
	}

	var summary []string
	for uri, b := range acceptedByURI {
		filePath := lsp.URItoPath(uri)
		if err := lsp.ApplyEditsToPath(filePath, b.edits); err != nil {
			summary = append(summary, fmt.Sprintf("  %s: failed to apply edits: %v", b.primaryServer, err))
			continue
		}
		rel := lsp.URItoRelativePath(uri, workingDir)
		summary = append(summary, fmt.Sprintf("  %s: applied %d edit%s to %s", b.primaryServer, len(b.edits), pluralS(len(b.edits)), rel))
		if b.discarded > 0 {
			var others []string
			for name := range b.conflictServers {
				others = append(others, name)
			}
			summary = append(summary, fmt.Sprintf("    note: discarded %d overlapping edit%s from %s (kept %s)",
				b.discarded, pluralS(b.discarded), strings.Join(others, ", "), b.primaryServer))
		}
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return nil, fmt.Errorf("mkdir parent of dest: %w", err)
	}
	if err := os.Rename(source, dest); err != nil {
		return nil, fmt.Errorf("rename: %w", err)
	}
	summary = append(summary, fmt.Sprintf("  Renamed %s → %s", sourceLabel, destLabel))

	// Without didClose, servers like gopls may retain stale URI references
	// for renamed files.
	sourceURIs := make([]string, 0, len(pairs))
	for _, p := range pairs {
		sourceURIs = append(sourceURIs, p.oldURI)
	}

	for _, spec := range relevantSpecs {
		if ctx.Err() != nil {
			break
		}
		c, err := reg.ForSpec(ctx, spec)
		if err != nil {
			continue
		}
		// Only close URIs this server actually has open; didClose on an
		// unopened document is a protocol violation.
		openSet := make(map[string]bool, 16)
		for _, u := range c.OpenURIs() {
			openSet[u] = true
		}
		for _, uri := range sourceURIs {
			if !openSet[uri] {
				continue
			}
			if nerr := c.Notify(ctx, "textDocument/didClose", map[string]any{
				"textDocument": map[string]string{"uri": uri},
			}); nerr != nil {
				serverNotes = append(serverNotes, fmt.Sprintf("  %s: didClose failed for %s: %v", spec.Name, lsp.URItoRelativePath(uri, workingDir), nerr))
			}
		}
		if nerr := c.Notify(ctx, "workspace/didRenameFiles", map[string]any{"files": pairMaps(pairs)}); nerr != nil {
			serverNotes = append(serverNotes, fmt.Sprintf("  %s: didRenameFiles failed: %v", spec.Name, nerr))
		}
	}

	if len(serverNotes) > 0 {
		summary = append(summary, "  Server notes:")
		summary = append(summary, serverNotes...)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Renamed %s → %s\n", fileCountLabel, destLabel)
	for _, line := range summary {
		fmt.Fprintf(&b, "%s\n", line)
	}
	return &tool.ToolResult{Data: b.String()}, nil
}

// renameFileNoServers handles the case where no LSP server claims this file
// type — we just perform the physical rename and report it.
func renameFileNoServers(source, dest string, pairs []fileRenamePair, isDir bool, workingDir string) (*tool.ToolResult, error) {
	sourceLabel := lsp.URItoRelativePath(lsp.FileToURI(source), workingDir)
	destLabel := lsp.URItoRelativePath(lsp.FileToURI(dest), workingDir)
	fileCountLabel := sourceLabel
	if isDir {
		fileCountLabel = fmt.Sprintf("%d file%s under %s", len(pairs), pluralS(len(pairs)), sourceLabel)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return nil, fmt.Errorf("mkdir parent of dest: %w", err)
	}
	if err := os.Rename(source, dest); err != nil {
		return nil, fmt.Errorf("rename: %w", err)
	}
	return &tool.ToolResult{Data: fmt.Sprintf("Renamed %s → %s\n  (no LSP server for these files; physical rename only)\n", fileCountLabel, destLabel)}, nil
}

// collectRelevantRenameServers returns specs whose FileExts match the source,
// dest, or any file inside the rename pair set.
func collectRelevantRenameServers(specs []lsp.ServerSpec, source, dest string, pairs []fileRenamePair) []lsp.ServerSpec {
	exts := make(map[string]bool)
	add := func(p string) {
		exts[filepath.Ext(p)] = true
	}
	add(source)
	add(dest)
	for _, p := range pairs {
		add(lsp.URItoPath(p.oldURI))
		add(lsp.URItoPath(p.newURI))
	}
	var out []lsp.ServerSpec
	for _, spec := range specs {
		for _, e := range spec.FileExts {
			if exts[e] {
				out = append(out, spec)
				break
			}
		}
	}
	return out
}

// pairMaps converts pairs to the LSP JSON shape [{oldUri, newUri}, ...].
func pairMaps(pairs []fileRenamePair) []map[string]string {
	out := make([]map[string]string, len(pairs))
	for i, p := range pairs {
		out[i] = map[string]string{"oldUri": p.oldURI, "newUri": p.newURI}
	}
	return out
}
