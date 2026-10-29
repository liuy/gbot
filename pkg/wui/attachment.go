package wui

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/liuy/gbot/pkg/media"
)

// inboundAttachment is the wire shape of one entry in user_message.attachments[].
// ID is the content-addressed media-cache id the upload route minted (or a
// cancel-restore re-staged); name/mime/size are the client's display metadata.
type inboundAttachment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Mime string `json:"mime"`
	Size int64  `json:"size"`
}

// classifyAttachment sniffs image magic bytes first; falls back to filename
// extension and treats the bytes as a document. For images, ext is derived
// from the sniffed mime (so a .png named "x.bin" still saves as .png); for
// documents, the filename extension is preserved (round-tripping through mime
// maps loses types like .pptx/.docx).
func classifyAttachment(data []byte, name, _ string) (mime, ext, kind string) {
	if sniffed := media.SniffImageMime(data); sniffed != "" {
		return sniffed, media.ExtFromMime(sniffed), "image"
	}
	return media.MimeFromExt(filepath.Ext(name)), preserveDocExt(name), "document"
}

// preserveDocExt returns the filename extension for document attachments,
// defaulting to .bin when no extension is present. MimeFromExt→ExtFromMime
// would collapse .pptx/.docx to .bin, so the original ext is preserved.
func preserveDocExt(name string) string {
	if e := filepath.Ext(name); e != "" {
		return e
	}
	return ".bin"
}

// attachmentIDRe is the grammar every minted id satisfies. Kept strict on the
// read side (no separators, lowercase hex, bounded ext) so hostile ids from
// the network can never walk the filesystem; the write side
// (sanitizeUploadExt) is what guarantees the server's own names round-trip.
var attachmentIDRe = regexp.MustCompile(`^([id]):([0-9a-f]{16}(\.[A-Za-z0-9]{1,10})?)$`)

// attachmentID tags a cache path with its category: "i:" → images/,
// "d:" → documents/. Resolution is then one joined path + one stat, with no
// ambiguity between the category dirs and no directory probing.
func attachmentID(cat media.Category, path string) string {
	prefix := "d:"
	if cat == media.CategoryImage {
		prefix = "i:"
	}
	return prefix + filepath.Base(path)
}

// parseAttachmentID validates an attachment id and returns its category and
// cache basename. ok=false for anything else — including documents/parse/
// names (they share the <sha16> naming but are derived data, never resolvable
// through an id).
func parseAttachmentID(id string) (cat media.Category, name string, ok bool) {
	m := attachmentIDRe.FindStringSubmatch(id)
	if m == nil {
		return "", "", false
	}
	if m[1] == "i" {
		return media.CategoryImage, m[2], true
	}
	return media.CategoryDocument, m[2], true
}

// asciiAlnum is the exact alphabet a sanitized extension may contain.
const asciiAlnum = "abcdefghijklmnopqrstuvwxyz0123456789"

// sanitizeUploadExt keeps cache names inside the grammar parseAttachmentID
// accepts: the store writes names as <sha16><ext>, and resolveAttachments
// later re-parses them. Non-ASCII, dotted, over-long or separator-bearing
// client extensions fall back to the store's ".bin".
func sanitizeUploadExt(ext string) string {
	if len(ext) < 2 || len(ext) > 11 { // "." plus 1..10 characters
		return ""
	}
	if ext[0] != '.' {
		return ""
	}
	lowered := strings.ToLower(ext[1:])
	for _, r := range lowered {
		if !strings.ContainsRune(asciiAlnum, r) {
			return ""
		}
	}
	return "." + lowered
}

// resolveAttachments maps the commit frame's attachment ids to content items
// by re-deriving everything from the cache: the id names the object, a stat
// proves it exists, and a bounded head read re-classifies it (the trustworthy
// extension is the one the store baked into the object name — the client's
// att.Name is free text that could claim .zip on a .pdf object). Size comes
// from the stat, not the client's declared number.
//
// Malformed or missing ids accumulate comma-joined so readLoop can answer
// "attachment not found: <ids>" without dispatching. A nil mediaCache makes
// every id a miss.
func (c *WUIConnector) resolveAttachments(atts []inboundAttachment) ([]inboundContent, string) {
	if c.mediaCache == nil {
		ids := make([]string, 0, len(atts))
		for _, att := range atts {
			ids = append(ids, att.ID)
		}
		return nil, strings.Join(ids, ", ")
	}
	var out []inboundContent
	var missing []string
	for _, att := range atts {
		cat, name, ok := parseAttachmentID(att.ID)
		if ok {
			path := filepath.Join(c.mediaCache.RootDir, string(cat), name)
			if st, err := os.Stat(path); err == nil && !st.IsDir() {
				if content, ok := classifyCachedObject(path, name, st.Size(), att.Name); ok {
					out = append(out, content)
					continue
				}
			}
		}
		missing = append(missing, att.ID)
	}
	if len(missing) > 0 {
		return nil, strings.Join(missing, ", ")
	}
	return out, ""
}

// classifyCachedObject reads a bounded head from the cached object and builds
// the inbound content item. The name handed to classifyAttachment is the cache
// basename (document mime derives from its extension); the display Name still
// comes from the commit metadata.
func classifyCachedObject(path, name string, size int64, displayName string) (inboundContent, bool) {
	f, err := os.Open(path)
	if err != nil {
		return inboundContent{}, false
	}
	head := make([]byte, uploadSniffHead)
	n, err := io.ReadFull(f, head)
	// Close immediately: the read is bounded, but holding the fd through a
	// 500 MiB object's lifetime would leak handles under load.
	_ = f.Close()
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return inboundContent{}, false
	}
	m, _, kind := classifyAttachment(head[:n], name, "")
	typ := "document"
	if kind == "image" {
		typ = "image"
	}
	return inboundContent{
		Type: typ,
		Source: inboundSource{
			Type: "file",
			Path: path,
			Mime: m,
			Name: displayName,
			Size: size,
		},
	}, true
}

// cachedID mints an attachment id for a path the cancel-restore path wants to
// re-stage. Bytes already inside the cache (images/ or documents/, named per
// the id grammar) bind directly with no IO; anything else — the common case
// for restored blocks over t.TempDir() paths — is copied in under the same
// per-kind cap the upload route enforces, so a restore cannot smuggle past
// it. Returns "" (and logs) when the bytes cannot be materialized; the caller
// drops the attachment.
func cachedID(store *media.Store, path, kind string) string {
	if store == nil {
		return ""
	}
	if rel, err := filepath.Rel(store.RootDir, path); err == nil {
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) == 2 && (parts[0] == string(media.CategoryImage) || parts[0] == string(media.CategoryDocument)) {
			prefix := "d:"
			if parts[0] == string(media.CategoryImage) {
				prefix = "i:"
			}
			if _, _, ok := parseAttachmentID(prefix + parts[1]); ok {
				return prefix + parts[1]
			}
		}
	}
	cat := media.CategoryDocument
	if kind == "image" {
		cat = media.CategoryImage
	}
	kindCap := uploadCapForKind(kind)
	st, err := os.Stat(path)
	if err != nil {
		slog.Warn("wui:restore attachment copy failed, dropping attachment", "error", err)
		return ""
	}
	if st.IsDir() || st.Size() > kindCap {
		slog.Warn("wui:restore attachment copy failed, dropping attachment",
			"error", fmt.Errorf("path %s is not a cacheable file (dir=%v size=%d cap=%d)", path, st.IsDir(), st.Size(), kindCap))
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		slog.Warn("wui:restore attachment copy failed, dropping attachment", "error", err)
		return ""
	}
	defer f.Close()
	// sanitizeUploadExt is load-bearing here too: cachedID mints ids, and an
	// unsanitized ext would produce an id parseAttachmentID rejects — a
	// restored chip that can never be committed.
	newPath, _, err := store.SaveReader(cat, sanitizeUploadExt(preserveDocExt(path)), f, kindCap)
	if err != nil {
		slog.Warn("wui:restore attachment copy failed, dropping attachment", "error", err)
		return ""
	}
	return attachmentID(cat, newPath)
}
