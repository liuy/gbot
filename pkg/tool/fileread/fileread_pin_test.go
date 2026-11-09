package fileread

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/markitdown"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/utils"
)

// Pin tests for the fileread.go mutation sweep; each assertion is exact so a
// single changed byte flips the test. Mutant line:mutator pairs are tracked in
// the campaign ledger.

// ---------------------------------------------------------------------------
// extension maps
// ---------------------------------------------------------------------------

func TestPin_ImageExtensionsJpegWebp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// A real JPEG under .jpeg must take the image path.
	jpegPath := filepath.Join(dir, "photo.jpeg")
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for i := range img.Pix {
		img.Pix[i] = byte(i)
	}
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jpegPath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: jpegPath}), nil)
	if err != nil {
		t.Fatalf("jpeg read: %v", err)
	}
	io, ok := res.Data.(ImageOutput)
	if !ok {
		t.Fatalf("jpeg Data type = %T, want ImageOutput", res.Data)
	}
	if io.Type != "image" || io.MimeType != "image/jpeg" {
		t.Errorf("jpeg output = %+v, want image/image/jpeg", io)
	}

	// Undecodable bytes under .webp must fail in the image path ("decode
	// image"), not fall through to the text path ("null bytes").
	webpPath := filepath.Join(dir, "pic.webp")
	if err := os.WriteFile(webpPath, []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Execute(context.Background(), mustPinJSON(t, Input{FilePath: webpPath}), nil)
	if err == nil || !strings.HasPrefix(err.Error(), "decode image:") {
		t.Fatalf("webp err = %v, want 'decode image:' prefix", err)
	}
}

// Binary extensions that are reachable through Execute (not shadowed by the
// convertible map or intercepted by the archive path).
func TestPin_BinaryExtensionRejected(t *testing.T) {
	t.Parallel()
	exts := []string{
		".ico", ".bmp", ".svg", ".mp3", ".mp4", ".wav", ".avi", ".mov", ".mkv",
		".exe", ".dll", ".so", ".dylib", ".a", ".o", ".obj", ".class", ".pyc",
		".par", ".pickle", ".whl",
	}
	dir := t.TempDir()
	for _, ext := range exts {
		p := filepath.Join(dir, "f"+ext)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: p}), nil)
		want := fmt.Sprintf("file has binary extension %s and cannot be read as text: %s", ext, p)
		if err == nil || err.Error() != want {
			t.Errorf("%s: err = %v, want %q", ext, err, want)
		}
	}
}

func TestPin_ConvertibleExtensionsRouted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// NUL byte content: convertible routing succeeds via markitdown
	// passthrough/convert, while a fall-through to the text path would be
	// rejected as binary. The .pdf case errors inside the converter.
	tt := []struct {
		ext      string
		wantErr  string // "" means success
		content  string
		validate func(t *testing.T, res *tool.ToolResult)
	}{
		{ext: ".pdf", content: "%PDF-garbage", wantErr: "convert .pdf:"},
		{ext: ".csv", content: "h\nv1\nv2", validate: func(t *testing.T, res *tool.ToolResult) {
			to, ok := res.Data.(TextOutput)
			if !ok || to.Content != "| h |\n| --- |\n| v1 |\n| v2 |" {
				t.Errorf("csv output = %#v, want markdown table", res.Data)
			}
		}},
	}
	for _, c := range tt {
		p := filepath.Join(dir, "f"+c.ext)
		if err := os.WriteFile(p, []byte(c.content), 0o644); err != nil {
			t.Fatal(err)
		}
		res, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: p}), nil)
		if c.wantErr != "" {
			if err == nil || !strings.HasPrefix(err.Error(), c.wantErr) {
				t.Errorf("%s: err = %v, want prefix %q", c.ext, err, c.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error: %v", c.ext, err)
			continue
		}
		c.validate(t, res)
	}
	// Passthrough/office converters reject NUL content inside the converter
	// ("convert .ext:"), proving the convertible branch was taken — a
	// fall-through would hit the binary-extension or null-bytes errors.
	for _, ext := range []string{".docx", ".epub", ".ipynb", ".doc", ".ppt", ".pptx", ".xls", ".xlsx"} {
		p := filepath.Join(dir, "g"+ext)
		if err := os.WriteFile(p, []byte("a\x00b"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: p}), nil)
		if err == nil || !strings.HasPrefix(err.Error(), "convert "+ext+":") {
			t.Errorf("%s with NUL content: err = %v, want 'convert %s:' prefix", ext, err, ext)
		}
	}
}

// ---------------------------------------------------------------------------
// device-path guard, expandPath, renderResult
// ---------------------------------------------------------------------------

func TestPin_BlockedDevicePathGuard(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"/srv/fd/0", "/var/tmp/fd/1", "/proc/fd", "/x/fd/3"} {
		if isBlockedDevicePath(p) {
			t.Errorf("isBlockedDevicePath(%q) = true, want false", p)
		}
	}
	for _, p := range []string{"/proc/self/fd/0", "/proc/123/fd/2", "/dev/zero", "/dev/stdout"} {
		if !isBlockedDevicePath(p) {
			t.Errorf("isBlockedDevicePath(%q) = false, want true", p)
		}
	}
}

func TestPin_ExpandPath(t *testing.T) {
	// No t.Parallel: t.Setenv.
	// Non-canonical absolute paths must pass through verbatim (filepath.Abs
	// would Clean them, which would merge distinct dedup keys).
	if got := expandPath("/tmp/x/../y"); got != "/tmp/x/../y" {
		t.Errorf("expandPath(/tmp/x/../y) = %q", got)
	}
	t.Setenv("HOME", "/home/pin")
	if got := expandPath("~/notes/a.txt"); got != "/home/pin/notes/a.txt" {
		t.Errorf("expandPath(~/notes/a.txt) = %q, want /home/pin/notes/a.txt", got)
	}
	if got := expandPath("~"); got != "/home/pin" {
		t.Errorf("expandPath(~) = %q, want /home/pin", got)
	}
}

func TestPin_RenderResultImageWithoutPath(t *testing.T) {
	t.Parallel()
	if got := renderResult(&ImageOutput{MimeType: "image/png"}); got != "Image (image/png)" {
		t.Errorf("renderResult(&ImageOutput{}) = %q, want 'Image (image/png)'", got)
	}
	if got := renderResult(ImageOutput{MimeType: "image/gif"}); got != "Image (image/gif)" {
		t.Errorf("renderResult(ImageOutput{}) = %q, want 'Image (image/gif)'", got)
	}
	if got := renderResult(&ImageOutput{FilePath: "/a.png", MimeType: "image/png", OriginalWidth: 3, OriginalHeight: 4}); got != "Image: /a.png (3x4)" {
		t.Errorf("renderResult image with path = %q", got)
	}
}

// ---------------------------------------------------------------------------
// ToolDef metadata
// ---------------------------------------------------------------------------

func TestPin_ToolDefMetadata(t *testing.T) {
	t.Parallel()
	tl := New()
	if tl.Name() != "Read" {
		t.Errorf("Name() = %q, want Read", tl.Name())
	}
	aliases := tl.Aliases()
	if len(aliases) != 3 || aliases[0] != "fileread" || aliases[1] != "read" || aliases[2] != "cat" {
		t.Errorf("Aliases() = %v, want [fileread read cat]", aliases)
	}
	var schema map[string]any
	if err := json.Unmarshal(tl.InputSchema(), &schema); err != nil {
		t.Fatalf("InputSchema not valid JSON: %v", err)
	}
	props, _ := schema["properties"].(map[string]any)
	if _, ok := props["file_path"]; !ok {
		t.Errorf("InputSchema missing file_path property: %v", schema)
	}
	desc, err := tl.Description(json.RawMessage(`{"file_path":"/x.go"}`))
	if err != nil || desc != "/x.go" {
		t.Errorf("Description = (%q, %v), want (/x.go, nil)", desc, err)
	}
	if tl.InterruptBehavior() != tool.InterruptCancel {
		t.Errorf("InterruptBehavior() = %v, want InterruptCancel", tl.InterruptBehavior())
	}
	if !strings.Contains(tl.Prompt(), "Reads a file from the local filesystem") {
		t.Errorf("Prompt() = %.60q, want the fileread prompt", tl.Prompt())
	}
	// Call routes through Call_: a file read must come back with content.
	dir := t.TempDir()
	p := filepath.Join(dir, "call.txt")
	if err := os.WriteFile(p, []byte("callbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := tl.Call(context.Background(), mustPinJSON(t, Input{FilePath: p}), nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if to, ok := res.Data.(TextOutput); !ok || to.Content != "callbody" {
		t.Errorf("Call result = %#v, want callbody text", res.Data)
	}
	if !tl.IsReadOnly(nil) || !tl.IsConcurrencySafe(nil) {
		t.Error("IsReadOnly/IsConcurrencySafe must be true")
	}
	if got := tl.CheckPermissions(json.RawMessage(`{"file_path":"/x"}`), nil); got == nil {
		t.Error("CheckPermissions returned nil decision")
	}
}

// ---------------------------------------------------------------------------
// DecodeResult
// ---------------------------------------------------------------------------

func decodeResult(t *testing.T, raw string) (any, error) {
	t.Helper()
	dr := New().(tool.ToolWithDecodeResult)
	return dr.DecodeResult([]byte(raw))
}

func TestPin_DecodeResultArrayGuard(t *testing.T) {
	t.Parallel()
	// Empty raw must be rejected by the length guard, not panic on raw[0].
	_, err := decodeResult(t, "")
	if err == nil || !strings.Contains(err.Error(), "expects array-form content") {
		t.Fatalf("empty raw err = %v, want array-form message", err)
	}
	// Non-array: message carries an 80-char truncated preview (81 chars of
	// input -> preview holds exactly 80).
	_, err = decodeResult(t, strings.Repeat("x", 81))
	if err == nil || !strings.Contains(err.Error(), strings.Repeat("x", 80)) {
		t.Fatalf("81-char raw err = %v, want 80-char preview", err)
	}
	if strings.Contains(err.Error(), strings.Repeat("x", 81)) {
		t.Errorf("preview not truncated to 80: %v", err)
	}
}

func TestPin_DecodeResultBlockDispatch(t *testing.T) {
	t.Parallel()
	// Array of a non-block value: the blocks unmarshal error must surface
	// (not the trailing "no decodable block" error).
	_, err := decodeResult(t, "[42]")
	if err == nil || !strings.Contains(err.Error(), "cannot unmarshal") {
		t.Fatalf("[42] err = %v, want json unmarshal error", err)
	}

	// Image block decodes with its type and media type intact.
	out, err := decodeResult(t, `[{"type":"image","source":{"type":"base64","media_type":"image/webp","data":"QQ=="}}]`)
	if err != nil {
		t.Fatal(err)
	}
	img, ok := out.(*ImageOutput)
	if !ok {
		t.Fatalf("image decode type = %T, want *ImageOutput", out)
	}
	if img.Type != "image" || img.MimeType != "image/webp" {
		t.Errorf("image decode = %+v, want type image / image/webp", *img)
	}

	// Plain text block decodes with Type set.
	out, err = decodeResult(t, `[{"type":"text","text":"hello"}]`)
	if err != nil {
		t.Fatal(err)
	}
	to, ok := out.(*TextOutput)
	if !ok {
		t.Fatalf("text decode type = %T, want *TextOutput", out)
	}
	if to.Type != "text" || to.Content != "hello" {
		t.Errorf("text decode = %+v, want type text / hello", *to)
	}

	// Empty text block is skipped: no decodable block error, not an empty
	// TextOutput.
	if _, err = decodeResult(t, `[{"type":"text","text":""}]`); err == nil || !strings.Contains(err.Error(), "no decodable block") {
		t.Errorf("empty text block err = %v, want no-decodable error", err)
	}
	// A block of any other type is skipped even when it carries text.
	if _, err = decodeResult(t, `[{"type":"thinking","text":"nope"}]`); err == nil || !strings.Contains(err.Error(), "no decodable block") {
		t.Errorf("non-text block err = %v, want no-decodable error", err)
	}
}

func TestPin_DecodeResultDualFormat(t *testing.T) {
	t.Parallel()
	// Leading-space JSON is NOT the legacy dual format (the wire never
	// emitted leading whitespace); it must come back verbatim as content.
	out, err := decodeResult(t, `[{"type":"text","text":" {\"type\":\"text\",\"content\":\"z\"}"}]`)
	if err != nil {
		t.Fatal(err)
	}
	to, _ := out.(*TextOutput)
	if to.Content != " {\"type\":\"text\",\"content\":\"z\"}" {
		t.Errorf("leading-space JSON content = %q, want raw text", to.Content)
	}

	// JSON with a decoded "type" but trailing garbage does not enter the
	// legacy branch (unmarshal fails) and comes back verbatim.
	out, err = decodeResult(t, `[{"type":"text","text":"{\"type\":\"file_unchanged\",bogus"}]`)
	if err != nil {
		t.Fatal(err)
	}
	to, _ = out.(*TextOutput)
	if to.Content != "{\"type\":\"file_unchanged\",bogus" {
		t.Errorf("partial-write JSON content = %q, want raw text", to.Content)
	}

	// Valid JSON without a "type" key is not dual format either.
	out, err = decodeResult(t, `[{"type":"text","text":"{\"content\":\"q\"}"}]`)
	if err != nil {
		t.Fatal(err)
	}
	to, _ = out.(*TextOutput)
	if to.Content != "{\"content\":\"q\"}" {
		t.Errorf("typeless JSON content = %q, want raw text", to.Content)
	}

	// A dual-format block that matches no case must not stop the scan:
	// the following plain text block is still returned.
	out, err = decodeResult(t, `[{"type":"text","text":"{\"type\":\"other\",\"filePath\":\"/f\"}"},{"type":"text","text":"after"}]`)
	if err != nil {
		t.Fatal(err)
	}
	to, _ = out.(*TextOutput)
	if to.Content != "after" {
		t.Errorf("post-legacy-block content = %q, want after", to.Content)
	}

	// The legacy file_unchanged dual format still decodes.
	out, err = decodeResult(t, `[{"type":"text","text":"{\"type\":\"file_unchanged\",\"filePath\":\"/f\"}"}]`)
	if err != nil {
		t.Fatal(err)
	}
	fu, ok := out.(*FileUnchangedOutput)
	if !ok || fu.Type != "file_unchanged" || fu.FilePath != "/f" {
		t.Fatalf("file_unchanged decode = %#v", out)
	}
}

// ---------------------------------------------------------------------------
// Execute input/permission/wrap paths
// ---------------------------------------------------------------------------

func mustPinJSON(t *testing.T, in Input) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPin_ExecuteParseInputWrap(t *testing.T) {
	t.Parallel()
	_, err := Execute(context.Background(), json.RawMessage(`{"file_path": 1}`), nil)
	if err == nil {
		t.Fatal("want error for bad input")
	}
	if _, ok := errors.AsType[*json.UnmarshalTypeError](err); !ok {
		t.Errorf("err = %v, want wrapped *json.UnmarshalTypeError (%%w)", err)
	}
	// Bad input must be a parse error, not "file_path is required": the
	// unmarshal guard fires first.
	if !strings.HasPrefix(err.Error(), "parse input:") {
		t.Errorf("err = %v, want 'parse input:' prefix", err)
	}
}

func TestPin_JoinGuardOperands(t *testing.T) {
	// No t.Parallel: t.Setenv.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir)

	// "~" must NOT be joined onto WorkingDir (it resolves to HOME, a dir).
	_, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: "~"}), &tool.ToolUseContext{WorkingDir: dir})
	if err == nil || !strings.HasPrefix(err.Error(), "path is a directory") {
		t.Fatalf("~ err = %v, want 'path is a directory'", err)
	}

	// "~/" paths resolve against HOME even with a WorkingDir set.
	res, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: "~/f.txt"}), &tool.ToolUseContext{WorkingDir: "/nonexistent-wd"})
	if err != nil || res == nil {
		t.Fatalf("~/f.txt with wd: res=%v err=%v", res, err)
	}

	// Absolute paths ignore WorkingDir (Join would produce wd/abs).
	res, err = Execute(context.Background(), mustPinJSON(t, Input{FilePath: filepath.Join(dir, "f.txt")}), &tool.ToolUseContext{WorkingDir: "/nonexistent-wd"})
	if err != nil || res == nil {
		t.Fatalf("abs path with wd: res=%v err=%v", res, err)
	}
}

func TestPin_JoinGuardNilTctxRelative(t *testing.T) {
	// No t.Parallel: process-wide chdir.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	// A relative path with a nil tctx must resolve via cwd, not panic.
	res, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: "f.txt"}), nil)
	if err != nil || res == nil {
		t.Fatalf("relative nil-tctx: res=%v err=%v", res, err)
	}
}

func TestPin_SqliteHandledResultReturned(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "pin.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO users (id, name) VALUES (1, 'ada')"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	res, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: dbPath + ":users"}), nil)
	if err != nil {
		t.Fatalf("sqlite read: %v", err)
	}
	if res == nil {
		t.Fatal("sqlite handled path returned nil result")
	}
	to, ok := res.Data.(TextOutput)
	if !ok {
		t.Fatalf("sqlite Data type = %T, want TextOutput", res.Data)
	}
	if !strings.Contains(to.Content, "users") || !strings.Contains(to.Content, "ada") {
		t.Errorf("sqlite content = %q, want users table with ada row", to.Content)
	}
	if to.Type != "text" {
		t.Errorf("sqlite output Type = %q, want text", to.Type)
	}

	// A handled sqlite path with a bad member must surface the error, not
	// swallow it.
	_, err = Execute(context.Background(), mustPinJSON(t, Input{FilePath: dbPath + ":missing_table"}), nil)
	if err == nil || !strings.Contains(err.Error(), "missing_table") {
		t.Fatalf("sqlite missing-table err = %v, want error naming missing_table", err)
	}
}

func TestPin_StatAndReadPermissionWraps(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("chmod-based permission tests are void as root")
	}
	dir := t.TempDir()
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Unstatable: stat error must wrap the *fs.PathError (errors.Is works).
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	_, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: p}), nil)
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("stat err = %v, want errors.Is fs.ErrPermission", err)
	}
	_ = os.Chmod(dir, 0o755)

	// Unreadable file: stat succeeds, ReadFile fails; full read.
	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
	_, err = Execute(context.Background(), mustPinJSON(t, Input{FilePath: p}), nil)
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("read err = %v, want errors.Is fs.ErrPermission", err)
	}
	// Same via the range path.
	_, err = Execute(context.Background(), mustPinJSON(t, Input{FilePath: p, Offset: 1, Limit: 1}), nil)
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("range read err = %v, want errors.Is fs.ErrPermission", err)
	}

	// Undecodable image: decode error wraps the image package error.
	imgPath := filepath.Join(dir, "bad.png")
	if err := os.WriteFile(imgPath, []byte("\x89PNG\r\n\x1a\ngarbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Execute(context.Background(), mustPinJSON(t, Input{FilePath: imgPath}), nil)
	if !strings.HasPrefix(err.Error(), "decode image:") {
		t.Fatalf("bad png err = %v, want 'decode image:' prefix", err)
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("decode image err not wrapped (%%w): %v", err)
	}

	// Unreadable image file: ReadFile error wraps fs.ErrPermission.
	var jpegBuf bytes.Buffer
	if err := jpeg.Encode(&jpegBuf, image.NewRGBA(image.Rect(0, 0, 1, 1)), nil); err != nil {
		t.Fatal(err)
	}
	lockedImg := filepath.Join(dir, "locked.jpg")
	if err := os.WriteFile(lockedImg, jpegBuf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lockedImg, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(lockedImg, 0o644) })
	_, err = Execute(context.Background(), mustPinJSON(t, Input{FilePath: lockedImg}), nil)
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("unreadable image err = %v, want errors.Is fs.ErrPermission", err)
	}
}

// ---------------------------------------------------------------------------
// text file reads: content, wire fields, slicing, dedup state
// ---------------------------------------------------------------------------

func TestPin_TextFileFullReadFields(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("l1\nl2\nl3"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: p}), nil)
	if err != nil {
		t.Fatal(err)
	}
	to, ok := res.Data.(TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want TextOutput", res.Data)
	}
	want := TextOutput{Type: "text", FilePath: p, Content: "l1\nl2\nl3", NumLines: 3, StartLine: 1, TotalLines: 3}
	if to != want {
		t.Errorf("full read = %+v, want %+v", to, want)
	}
}

func TestPin_TextFileRangeReads(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("a\nb\nc\nd"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(in Input) TextOutput {
		t.Helper()
		res, err := Execute(context.Background(), mustPinJSON(t, in), nil)
		if err != nil {
			t.Fatal(err)
		}
		to, ok := res.Data.(TextOutput)
		if !ok {
			t.Fatalf("Data type = %T, want TextOutput", res.Data)
		}
		return to
	}
	// offset=2, limit=0: rest of file from line 2.
	if got := run(Input{FilePath: p, Offset: 2}); got.Content != "b\nc\nd" || got.StartLine != 2 || got.NumLines != 3 || got.TotalLines != 4 || got.Type != "text" || got.FilePath != p {
		t.Errorf("offset=2 read = %+v", got)
	}
	// offset=1, limit=1: exactly the first line.
	if got := run(Input{FilePath: p, Offset: 1, Limit: 1}); got.Content != "a" || got.NumLines != 1 || got.StartLine != 1 {
		t.Errorf("offset=1 limit=1 read = %+v", got)
	}
	// CRLF normalization applies to range reads too.
	crlf := filepath.Join(dir, "crlf.txt")
	if err := os.WriteFile(crlf, []byte("x\r\ny"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := run(Input{FilePath: crlf, Offset: 2}); got.Content != "y" || got.TotalLines != 2 {
		t.Errorf("crlf offset=2 read = %+v", got)
	}
}

func TestPin_TextFileDedupState(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	tctx := &tool.ToolUseContext{}
	if _, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: p, Offset: 2, Limit: 1}), tctx); err != nil {
		t.Fatal(err)
	}
	state, ok := tctx.ReadFileState[p]
	if !ok {
		t.Fatalf("ReadFileState missing key %q: %v", p, tctx.ReadFileState)
	}
	want := tool.FileState{Content: "b", Timestamp: info.ModTime().UnixMilli(), Offset: 2, Limit: 1, IsPartialView: true}
	if state != want {
		t.Errorf("state = %+v, want %+v", state, want)
	}

	// Second read with a different offset must NOT dedup.
	res, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: p, Offset: 3}), tctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.Data.(TextOutput); !ok {
		t.Fatalf("offset-differs read = %T, want TextOutput (no dedup)", res.Data)
	}
	// Second read with a different limit must NOT dedup either.
	res, err = Execute(context.Background(), mustPinJSON(t, Input{FilePath: p, Limit: 2}), tctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.Data.(TextOutput); !ok {
		t.Fatalf("limit-differs read = %T, want TextOutput (no dedup)", res.Data)
	}

	// Same offset with a different limit must NOT dedup (limit is part of
	// the dedup key even when the recorded view was unsliced).
	sameOff := &tool.ToolUseContext{}
	if _, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: p, Offset: 2}), sameOff); err != nil {
		t.Fatal(err)
	}
	res, err = Execute(context.Background(), mustPinJSON(t, Input{FilePath: p, Offset: 2, Limit: 1}), sameOff)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.Data.(TextOutput); !ok {
		t.Fatalf("same-offset-diff-limit read = %T, want TextOutput (no dedup)", res.Data)
	}

	// An identical repeat of the SAME full read dedups to the stub with its
	// type intact.
	dedupCtx := &tool.ToolUseContext{}
	if _, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: p}), dedupCtx); err != nil {
		t.Fatal(err)
	}
	res, err = Execute(context.Background(), mustPinJSON(t, Input{FilePath: p}), dedupCtx)
	if err != nil {
		t.Fatal(err)
	}
	fu, ok := res.Data.(FileUnchangedOutput)
	if !ok {
		t.Fatalf("identical read = %T, want FileUnchangedOutput", res.Data)
	}
	if fu.Type != "file_unchanged" || fu.FilePath != p {
		t.Errorf("file_unchanged = %+v", fu)
	}
}

// pinTokenContent returns content whose EstimateTokens is exactly want, or
// fails the test (the estimator is a pure, monotonic function of the repeat
// count, so a short scan around the binary-searched crossing suffices).
func pinTokenContent(t *testing.T, want int) string {
	t.Helper()
	est := func(n int) int { return utils.EstimateTokens(strings.Repeat("a ", n)) }
	lo, hi := 1, 1<<20
	if est(hi) < want {
		t.Fatalf("no repeat count up to %d yields %d tokens", hi, want)
	}
	for lo < hi {
		mid := (lo + hi) / 2
		if est(mid) < want {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	for n := lo - 2; n <= lo+2; n++ {
		if n > 0 && est(n) == want {
			return strings.Repeat("a ", n)
		}
	}
	t.Fatalf("no repeat count yields exactly %d tokens (crossing at %d)", want, lo)
	return ""
}

func TestPin_TokenBoundExact(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "exact.txt")
	if err := os.WriteFile(p, []byte(pinTokenContent(t, MaxFileReadTokens)), 0o644); err != nil {
		t.Fatal(err)
	}
	// Exactly MaxFileReadTokens passes the bound (> comparison).
	if _, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: p}), nil); err != nil {
		t.Errorf("exact-token read err = %v, want nil", err)
	}
}

func TestPin_TokenBoundExceeded(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "big.txt")
	content := pinTokenContent(t, MaxFileReadTokens+50)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	input := mustPinJSON(t, Input{FilePath: p})

	_, err := Execute(context.Background(), input, nil)
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum allowed tokens") {
		t.Fatalf("nil-tctx err = %v, want token-bound error", err)
	}
	// A capped context with state must fail BEFORE recording the read —
	// a rejected over-read leaving a stale ReadFileState entry would trip
	// fileedit's must-read-first gate for a file the model never read.
	capped := &tool.ToolUseContext{ReadFileState: map[string]tool.FileState{}}
	_, err = Execute(context.Background(), input, capped)
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum allowed tokens") {
		t.Fatalf("capped-tctx err = %v, want token-bound error", err)
	}
	if len(capped.ReadFileState) != 0 {
		t.Errorf("rejected over-read recorded ReadFileState entries: %v", capped.ReadFileState)
	}
	// Uncapped context lifts the bound.
	if _, err := Execute(context.Background(), input, &tool.ToolUseContext{UncappedOutput: true}); err != nil {
		t.Errorf("uncapped-tctx err = %v, want nil", err)
	}
}

// ---------------------------------------------------------------------------
// executeDocument: conversion outputs and offset/limit on markdown
// ---------------------------------------------------------------------------

func TestPin_DocumentConvertOutputs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	empty := filepath.Join(dir, "empty.csv")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: empty}), nil)
	want := fmt.Sprintf("conversion produced no output: %s", empty)
	if err == nil || err.Error() != want {
		t.Fatalf("empty conversion err = %v, want %q", err, want)
	}

	bad := filepath.Join(dir, "bad.pdf")
	if err := os.WriteFile(bad, []byte("%PDF-garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Execute(context.Background(), mustPinJSON(t, Input{FilePath: bad}), nil)
	if err == nil || !strings.HasPrefix(err.Error(), "convert .pdf:") {
		t.Fatalf("bad pdf err = %v, want 'convert .pdf:' prefix", err)
	}
	if errors.Unwrap(errors.Unwrap(err)) == nil && errors.Unwrap(err) == nil {
		t.Errorf("convert err not wrapped (%%w): %v", err)
	}
}

func TestPin_DocumentMarkdownLineAccounting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// markitdown's csv output has no trailing newline; epub garbage is
	// passed through verbatim, so a trailing newline can be planted.
	csv := filepath.Join(dir, "rows.csv")
	if err := os.WriteFile(csv, []byte("h\nv1\nv2"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: csv}), nil)
	if err != nil {
		t.Fatal(err)
	}
	to, _ := res.Data.(TextOutput)
	if to.Content != "| h |\n| --- |\n| v1 |\n| v2 |" || to.TotalLines != 4 || to.NumLines != 4 || to.Type != "text" || to.FilePath != csv {
		t.Errorf("csv doc output = %+v", to)
	}

	// A minimal real docx converts to paragraph markdown (no trailing
	// newline): "hello\n\nworld" is 3 lines.
	docx := filepath.Join(dir, "real.docx")
	if err := makeZip(docx, map[string]string{
		"[Content_Types].xml": `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/></Types>`,
		"word/document.xml":   `<?xml version="1.0" encoding="UTF-8"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>hello</w:t></w:r></w:p><w:p><w:r><w:t>world</w:t></w:r></w:p></w:body></w:document>`,
	}); err != nil {
		t.Fatal(err)
	}
	res, err = Execute(context.Background(), mustPinJSON(t, Input{FilePath: docx}), nil)
	if err != nil {
		t.Fatal(err)
	}
	to, _ = res.Data.(TextOutput)
	if to.Content != "hello\n\nworld" || to.TotalLines != 3 || to.NumLines != 3 {
		t.Errorf("docx doc output = %+v, want hello/world 3-line accounting", to)
	}

	// offset/limit on the markdown: line 2 only.
	res, err = Execute(context.Background(), mustPinJSON(t, Input{FilePath: csv, Offset: 2, Limit: 1}), nil)
	if err != nil {
		t.Fatal(err)
	}
	to, _ = res.Data.(TextOutput)
	if to.Content != "| --- |" || to.NumLines != 1 || to.StartLine != 2 || to.TotalLines != 4 {
		t.Errorf("csv doc offset=2 limit=1 = %+v", to)
	}
}

// Over-token converted documents have no in-converter token check; the
// boundTextOutput layer is the only guard for the document path.
func TestPin_TokenBoundDocument(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	big := filepath.Join(dir, "big.csv")
	if err := os.WriteFile(big, bytes.Repeat([]byte("a,b\n"), 40000), 0o644); err != nil {
		t.Fatal(err)
	}
	input := mustPinJSON(t, Input{FilePath: big})

	_, err := Execute(context.Background(), input, &tool.ToolUseContext{})
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum allowed tokens") {
		t.Fatalf("capped doc err = %v, want token-bound error", err)
	}
	// The bound must consult tctx without panicking on a nil context.
	_, err = Execute(context.Background(), input, nil)
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum allowed tokens") {
		t.Fatalf("nil-tctx doc err = %v, want token-bound error", err)
	}
	if _, err := Execute(context.Background(), input, &tool.ToolUseContext{UncappedOutput: true}); err != nil {
		t.Errorf("uncapped doc err = %v, want nil", err)
	}

	// A document whose markdown estimates exactly MaxFileReadTokens passes
	// the bound (strict > comparison). Row count is binary-searched through
	// the converter itself so the expectation tracks the real markdown.
	m := markitdown.New()
	exact := filepath.Join(dir, "exact.csv")
	mdTokens := func(rows int) int {
		content := "h\n" + strings.Repeat("r\n", rows)
		p := filepath.Join(dir, "probe.csv")
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		res, err := m.ConvertFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return utils.EstimateTokens(res.Markdown)
	}
	lo, hi := 1, 40000
	for lo < hi {
		mid := (lo + hi) / 2
		if mdTokens(mid) < MaxFileReadTokens {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	found := false
	for n := lo - 2; n <= lo+2 && !found; n++ {
		if n > 0 && mdTokens(n) == MaxFileReadTokens {
			if err := os.WriteFile(exact, []byte("h\n"+strings.Repeat("r\n", n)), 0o644); err != nil {
				t.Fatal(err)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("no row count yields exactly %d markdown tokens (crossing at %d)", MaxFileReadTokens, lo)
	}
	if _, err := Execute(context.Background(), mustPinJSON(t, Input{FilePath: exact}), &tool.ToolUseContext{}); err != nil {
		t.Errorf("exact-token doc err = %v, want nil", err)
	}
	_ = os.Remove(filepath.Join(dir, "probe.csv"))
}

// ---------------------------------------------------------------------------
// wire blocks (FormatWireBlocks pass-through already covered elsewhere); this
// pins the empty-file and offset-beyond-end warnings used by the text path.
// ---------------------------------------------------------------------------

func TestPin_WireTextWarnings(t *testing.T) {
	t.Parallel()
	wb := New().(tool.ToolWithWireBlocks)
	blocks := wb.FormatWireBlocks(TextOutput{Type: "text", TotalLines: 0})
	if len(blocks) != 1 || !strings.Contains(blocks[0].Text, "contents are empty") {
		t.Errorf("empty warning blocks = %+v", blocks)
	}
	blocks = wb.FormatWireBlocks(TextOutput{Type: "text", StartLine: 9, TotalLines: 4})
	if len(blocks) != 1 || blocks[0].Text != "<system-reminder>Warning: the file exists but is shorter than the provided offset (9). The file has 4 lines.</system-reminder>" {
		t.Errorf("offset warning blocks = %+v", blocks)
	}
	// Line-numbered text: numbering starts at StartLine.
	blocks = wb.FormatWireBlocks(TextOutput{Type: "text", Content: "x\ny", StartLine: 7})
	if len(blocks) != 1 || blocks[0].Text != "7\tx\n8\ty" {
		t.Errorf("numbered blocks = %+v", blocks)
	}
	if b := blocks[0]; b.Type != "text" {
		t.Errorf("text block type = %q", b.Type)
	}
}
