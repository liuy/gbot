package wui

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"github.com/liuy/gbot/pkg/config"
)

// avatarMaxBytes caps PUT bodies. The frontend crops to 256×256 before
// uploading, so real payloads are tens of KB — the cap only stops abuse.
const avatarMaxBytes = 2 << 20

// RegisterAvatarRoutes mounts the avatar HTTP surface. The file is the
// interface: ~/.gbot/avatars/{agent,user}.png exist = custom avatar,
// absent = default rendering.
//
//   - GET    /api/avatar/{who} — serve the file (sniffed Content-Type, ETag)
//   - PUT    /api/avatar/{who} — atomically replace it (raw image bytes)
//   - DELETE /api/avatar/{who} — remove it (idempotent 204)
func RegisterAvatarRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/avatar/{who}", handleAvatarGet)
	mux.HandleFunc("PUT /api/avatar/{who}", handleAvatarPut)
	mux.HandleFunc("DELETE /api/avatar/{who}", handleAvatarDelete)
}

// validAvatarWho whitelists the two avatar identities; anything else 404s
// rather than creating surprising files.
func validAvatarWho(who string) bool {
	return who == "agent" || who == "user"
}

// avatarFilePath resolves the avatar file under the live config dir, so
// tests can isolate via HOME.
func avatarFilePath(who string) (string, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "avatars", who+".png"), nil
}

// avatarNotFound keeps the 404 body empty: <img> consumers only care about
// the status, and an empty body lets tests assert absence byte-exactly.
func avatarNotFound(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotFound)
}

func handleAvatarGet(w http.ResponseWriter, r *http.Request) {
	who := r.PathValue("who")
	if !validAvatarWho(who) {
		avatarNotFound(w)
		return
	}
	path, err := avatarFilePath(who)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	f, err := os.Open(path)
	if err != nil {
		avatarNotFound(w)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		// A directory squatting on the file name is an abnormal state —
		// fall back to "no avatar", never crash.
		avatarNotFound(w)
		return
	}
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	// Sniff from bytes, not the extension: the filename is fixed <who>.png
	// but PUT may legally store JPEG bytes (oversize-PNG fallback).
	w.Header().Set("Content-Type", http.DetectContentType(head[:n]))
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", fmt.Sprintf(`"%x-%x"`, st.ModTime().UnixNano(), st.Size()))
	http.ServeContent(w, r, who+".png", st.ModTime(), f)
}

func handleAvatarPut(w http.ResponseWriter, r *http.Request) {
	who := r.PathValue("who")
	if !validAvatarWho(who) {
		avatarNotFound(w)
		return
	}
	// Missing header and unparseable header take the same path: both mean
	// "the client did not declare a supported image type".
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || (mt != "image/png" && mt != "image/jpeg") {
		errorJSON(w, http.StatusUnsupportedMediaType, "unsupported content type")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, avatarMaxBytes)
	data, err := io.ReadAll(r.Body)
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		errorJSON(w, http.StatusRequestEntityTooLarge, "avatar exceeds 2MB limit")
		return
	}
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(data) == 0 {
		errorJSON(w, http.StatusBadRequest, "empty body")
		return
	}
	if !avatarMagicMatches(mt, data) {
		errorJSON(w, http.StatusBadRequest, "content does not match declared image type")
		return
	}
	path, err := avatarFilePath(who)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		errorJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Temp + rename so a crash mid-write never leaves a truncated avatar.
	tmp, err := os.CreateTemp(dir, ".avatar-*.tmp")
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	tmpName := tmp.Name()
	// Best-effort cleanup on the failure paths below: the original error is
	// the report, a second failure while discarding the temp file adds
	// nothing (errcheck silence is deliberate).
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		errorJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		errorJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		_ = os.Remove(tmpName)
		errorJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		errorJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// avatarMagicMatches cross-checks the declared type against the actual
// bytes — a text/plain-that-says-image/png body must not become a file the
// browser then fails to decode.
func avatarMagicMatches(mt string, data []byte) bool {
	switch mt {
	case "image/png":
		return len(data) >= 8 && bytes.Equal(data[:8],
			[]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A})
	case "image/jpeg":
		return len(data) >= 3 && bytes.Equal(data[:3], []byte{0xFF, 0xD8, 0xFF})
	}
	return false
}

func handleAvatarDelete(w http.ResponseWriter, r *http.Request) {
	who := r.PathValue("who")
	if !validAvatarWho(who) {
		avatarNotFound(w)
		return
	}
	path, err := avatarFilePath(who)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Absent file is the same end state as removed — idempotent 204.
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		errorJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
