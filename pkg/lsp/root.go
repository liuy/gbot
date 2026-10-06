package lsp

import (
	"os"
	"path/filepath"
	"strings"
)

// rootMarkersByExt maps a file extension to the file that marks the directory a
// language server must be rooted at. Extensions absent here get no root of their
// own: their files stay on the launch workspace's server.
var rootMarkersByExt = map[string][]string{
	".go":  {"go.mod"},
	".ts":  {"package.json"},
	".tsx": {"package.json"},
	".js":  {"package.json"},
	".jsx": {"package.json"},
	".mjs": {"package.json"},
	".cjs": {"package.json"},
	".rs":  {"Cargo.toml"},
}

// cleanAbs makes a path comparable against a root: absolute, cleaned, and
// symlink-resolved when possible so a symlinked root and a real path do not
// produce two servers for one project. EvalSymlinks failing is not an error —
// a path that does not exist yet is still usable as a comparable path.
func cleanAbs(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved)
	}
	return abs
}

// pathWithin reports whether p is root or a descendant of root. The comparison
// is prefix-plus-separator rather than a bare prefix: /a/bc is not inside /a/b.
func pathWithin(p, root string) bool {
	if root == "" {
		return false
	}
	p = cleanAbs(p)
	root = cleanAbs(root)
	if root == "/" {
		return strings.HasPrefix(p, "/")
	}
	return p == root || strings.HasPrefix(p, root+"/")
}

// projectRootFor walks up from path looking for a marker for ext. Returns ""
// when ext has no markers or no ancestor has one. A Stat error other than
// "not exists" is treated as "not a marker" rather than aborting the walk: a
// permission error on one ancestor must not lose the project root.
func projectRootFor(path, ext string) string {
	markers := rootMarkersByExt[ext]
	if len(markers) == 0 {
		return ""
	}
	dir := filepath.Dir(cleanAbs(path))
	for {
		for _, marker := range markers {
			info, err := os.Stat(filepath.Join(dir, marker))
			if err == nil && info.Mode().IsRegular() {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
