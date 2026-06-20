package fileread

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/toolresult"
	"github.com/mholt/archives"
)

// Supported archive extensions. mholt/archives auto-detects the underlying
// format, so we only need to gate path parsing by extension.
var archiveExtensions = []string{
	".tar.gz", ".tgz",
	".tar.xz", ".txz",
	".tar.bz2", ".tbz2",
	".tar.zst",
	".tar.lz4",
	".tar",
	".zip",
	".gz",
	".bz2",
	".xz",
	".zst",
	".lz4",
	".7z",
	".rar",
}

// archiveMaxMemberBytes caps a single extracted member's size to avoid OOM
// from malicious metadata. omp: MAX_ARCHIVE_MEMBER_BYTES = 64 MiB.
const archiveMaxMemberBytes int64 = 64 * 1024 * 1024

// archivePathCandidate is a parsed archive path + inner subpath.
type archivePathCandidate struct {
	archivePath string
	subPath     string
}

// parseArchivePathCandidates finds every plausible archive split in the input
// path, longest archive path first (more specific wins). omp: parseArchivePathCandidates.
func parseArchivePathCandidates(filePath string) []archivePathCandidate {
	normalized := strings.ToLower(strings.ReplaceAll(filePath, "\\", "/"))
	seen := map[string]bool{}
	var out []archivePathCandidate

	for _, ext := range archiveExtensions {
		idx := strings.Index(normalized, ext)
		for idx >= 0 {
			end := idx + len(ext)
			archivePath := filePath[:end]
			subPath := strings.TrimLeft(strings.ReplaceAll(filePath, "\\", "/")[end:], ":")
			key := archivePath + "\x00" + subPath
			if !seen[key] {
				seen[key] = true
				out = append(out, archivePathCandidate{archivePath: archivePath, subPath: subPath})
			}
			next := strings.Index(normalized[end:], ext)
			if next < 0 {
				break
			}
			idx = end + next
		}
	}

	sort.Slice(out, func(i, j int) bool {
		return len(out[i].archivePath) > len(out[j].archivePath)
	})
	return out
}

// tryArchivePath attempts to handle the input as an archive path.
// Returns (result, true, nil) if handled; (nil, false, nil) if not an archive
// path; (nil, true, err) on archive read error.
func tryArchivePath(ctx context.Context, in Input) (*tool.ToolResult, bool, error) {
	candidates := parseArchivePathCandidates(in.FilePath)
	if len(candidates) == 0 {
		return nil, false, nil
	}
	for _, c := range candidates {
		ap, err := filepath.Abs(c.archivePath)
		if err != nil {
			continue
		}
		info, err := os.Stat(ap)
		if err != nil || info.IsDir() {
			continue
		}
		fsys, err := archives.FileSystem(ctx, ap, nil)
		if err != nil {
			return nil, true, fmt.Errorf("open archive: %w", err)
		}
		result, err := readArchiveFS(fsys, c.subPath, in)
		if err != nil {
			return nil, true, err
		}
		return result, true, nil
	}
	return nil, false, nil
}

// readArchiveFS reads a directory listing or a member file from the archive.
func readArchiveFS(fsys fs.FS, subPath string, in Input) (*tool.ToolResult, error) {
	// Empty subPath → root.
	target := normalizeArchivePath(subPath)
	if target == "" {
		target = "."
	}

	entry, err := fsys.Open(target)
	if err != nil {
		return nil, fmt.Errorf("path '%s' not found inside archive", subPath)
	}
	defer entry.Close()

	info, err := entry.Stat()
	if err != nil {
		return nil, err
	}

	if info.IsDir() {
		dir, ok := entry.(fs.ReadDirFile)
		if !ok {
			return nil, fmt.Errorf("archive directory '%s' does not support ReadDir", subPath)
		}
		entries, err := dir.ReadDir(-1)
		if err != nil {
			return nil, fmt.Errorf("read archive directory '%s': %w", subPath, err)
		}
		content := formatArchiveEntries(entries)
		return &tool.ToolResult{Data: TextOutput{
			Type:     "text",
			FilePath: in.FilePath,
			Content:  content,
		}}, nil
	}

	// File member — read with size cap.
	limited := io.LimitReader(entry, archiveMaxMemberBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read archive member '%s': %w", subPath, err)
	}
	if int64(len(data)) > archiveMaxMemberBytes {
		return nil, fmt.Errorf("archive member '%s' exceeds %d bytes", subPath, archiveMaxMemberBytes)
	}

	if !isValidUTF8(data) {
		return &tool.ToolResult{Data: TextOutput{
			Type:     "text",
			FilePath: in.FilePath,
			Content:  fmt.Sprintf("[Cannot read binary archive entry '%s' (%d bytes)]", subPath, len(data)),
		}}, nil
	}

	content := string(data)
	if in.Offset > 1 || in.Limit > 0 {
		content = applyOffsetLimit(content, in.Offset, in.Limit)
	}
	return &tool.ToolResult{Data: TextOutput{
		Type:     "text",
		FilePath: in.FilePath,
		Content:  content,
	}}, nil
}

// normalizeArchivePath canonicalizes an entry path: strips leading "./",
// rejects ".." traversal, returns "" for root.
func normalizeArchivePath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimPrefix(p, "/")
	if p == "" || p == "." {
		return ""
	}
	cleaned := path.Clean(p)
	if strings.HasPrefix(cleaned, "..") || cleaned == ".." {
		return ""
	}
	return cleaned
}

// formatArchiveEntries renders directory entries as one line per item.
// Directories get trailing "/"; files get "(<size>)" when non-zero.
func formatArchiveEntries(entries []fs.DirEntry) string {
	if len(entries) == 0 {
		return "(empty archive directory)"
	}
	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})
	var lines []string
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			lines = append(lines, e.Name())
			continue
		}
		if e.IsDir() {
			lines = append(lines, e.Name()+"/")
			continue
		}
		if info.Size() > 0 {
			lines = append(lines, fmt.Sprintf("%s (%s)", e.Name(), toolresult.FormatFileSize(int(info.Size()))))
			continue
		}
		lines = append(lines, e.Name())
	}
	return strings.Join(lines, "\n")
}

// isValidUTF8 returns true if b looks like text — valid UTF-8 with no NUL bytes.
func isValidUTF8(b []byte) bool {
	if bytes.IndexByte(b, 0) >= 0 {
		return false
	}
	for i := 0; i < len(b); {
		c := b[i]
		var size int
		switch {
		case c < 0x80:
			size = 1
		case c&0xE0 == 0xC0:
			size = 2
		case c&0xF0 == 0xE0:
			size = 3
		case c&0xF8 == 0xF0:
			size = 4
		default:
			return false
		}
		if i+size > len(b) {
			return false
		}
		for j := 1; j < size; j++ {
			if b[i+j]&0xC0 != 0x80 {
				return false
			}
		}
		i += size
	}
	return true
}

// applyOffsetLimit returns lines [offset, offset+limit) of s. offset is 1-indexed.
func applyOffsetLimit(s string, offset, limit int) string {
	lines := strings.Split(s, "\n")
	if offset < 1 {
		offset = 1
	}
	start := offset - 1
	if start >= len(lines) {
		return ""
	}
	end := len(lines)
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	return strings.Join(lines[start:end], "\n")
}
