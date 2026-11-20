package wui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/memory/short"
)

func sendEngineRename(t *testing.T, ws *websocket.Conn, engineID, name string) {
	t.Helper()
	msg, _ := json.Marshal(map[string]string{"type": "engine_rename", "engineID": engineID, "name": name})
	if err := ws.WriteMessage(websocket.TextMessage, msg); err != nil {
		t.Fatalf("write engine_rename: %v", err)
	}
}

func TestEngineRename_UnknownEngineRejected(t *testing.T) {
	c, _, mgr, _ := deleteTestRig(t)
	ws := dialAndStore(t, c)
	defer ws.Close()
	sendEngineRename(t, ws, "ghost", "nope")
	msg, scope := decodeScopedError(t, nextFrameOfType(t, ws, "error"))
	if scope != "engine_rename" {
		t.Fatalf("scope = %q, want engine_rename", scope)
	}
	if msg != "unknown engine: ghost" {
		t.Fatalf("message = %q, want unknown engine wording", msg)
	}
	if mgr.Get("e2").Name != "agent-2" {
		t.Fatalf("e2 name = %q, want agent-2 untouched", mgr.Get("e2").Name)
	}
}

func TestEngineRename_EmptyNameRejected(t *testing.T) {
	c, _, mgr, _ := deleteTestRig(t)
	ws := dialAndStore(t, c)
	defer ws.Close()
	// Whitespace-only: the trim happens server-side, so an untrimmed frame
	// must still be rejected (an untrimmed name would register as-is).
	sendEngineRename(t, ws, "e2", "   ")
	msg, scope := decodeScopedError(t, nextFrameOfType(t, ws, "error"))
	if scope != "engine_rename" {
		t.Fatalf("scope = %q, want engine_rename", scope)
	}
	if msg != "engine name must not be empty" {
		t.Fatalf("message = %q, want empty-name wording", msg)
	}
	if mgr.Get("e2").Name != "agent-2" {
		t.Fatalf("e2 name = %q, want agent-2 untouched", mgr.Get("e2").Name)
	}
}

func TestEngineRename_NameLengthLimit(t *testing.T) {
	c, _, mgr, _ := deleteTestRig(t)
	ws := dialAndStore(t, c)
	defer ws.Close()

	// Exactly at the cap passes — an off-by-one (>= instead of >) would
	// reject the last legal name. The cap counts CHARACTERS, not bytes:
	// 64 CJK runes are 192 bytes and must still pass.
	sendEngineRename(t, ws, "e2", strings.Repeat("名", 64))
	raw := nextFrameOfType(t, ws, "engine_list")
	if !strings.Contains(string(raw), strings.Repeat("名", 64)) {
		t.Fatalf("engine_list = %s, want the 64-rune CJK name accepted", raw)
	}

	sendEngineRename(t, ws, "e2", strings.Repeat("字", 65))
	msg, scope := decodeScopedError(t, nextFrameOfType(t, ws, "error"))
	if scope != "engine_rename" {
		t.Fatalf("scope = %q, want engine_rename", scope)
	}
	if !strings.Contains(msg, "64") {
		t.Fatalf("message = %q, want the limit surfaced", msg)
	}
	if mgr.Get("e2").Name != strings.Repeat("名", 64) {
		t.Fatal("oversize rename must not change the name")
	}
}

// The house ValidateName contract (creation path) applies to renames too:
// duplicates, reserved ids and the agent-N grammar are rejected — a rename
// that minted an ambiguous name would break GetByName resolution.
func TestEngineRename_ValidateNameContract(t *testing.T) {
	c, _, mgr, _ := deleteTestRig(t)
	ws := dialAndStore(t, c)
	defer ws.Close()

	for _, tc := range []struct {
		name string
		want string
	}{
		{"main", "reserved"},   // main's own name is also the reserved id
		{"dream", "reserved"},  // reserved id
		{"agent-3", "agent-N"}, // counter-minted id namespace
	} {
		sendEngineRename(t, ws, "e2", tc.name)
		msg, scope := decodeScopedError(t, nextFrameOfType(t, ws, "error"))
		if scope != "engine_rename" {
			t.Fatalf("rename %q: scope = %q, want engine_rename", tc.name, scope)
		}
		if !strings.Contains(msg, tc.want) {
			t.Fatalf("rename %q: message = %q, want it to mention %q", tc.name, msg, tc.want)
		}
	}
	if mgr.Get("e2").Name != "agent-2" {
		t.Fatalf("name = %q, want the original untouched", mgr.Get("e2").Name)
	}

	// Renaming to the engine's OWN current name is a no-op success, not a
	// self-collision error.
	sendEngineRename(t, ws, "e2", "agent-2")
	raw := nextFrameOfType(t, ws, "engine_list")
	if !strings.Contains(string(raw), "agent-2") {
		t.Fatalf("same-name rename should be a no-op success, got %s", raw)
	}
}

func TestEngineRename_ReadOnlyEngineRejected(t *testing.T) {
	dir := t.TempDir()
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "main"})
	mgr.Add(&engine.EngineViewState{ID: "wx", Name: "wechat-1", ReadOnly: true})
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	c.mgr = mgr
	c.mock().projectDirFn = func() string { return dir }
	ws := dialAndStore(t, c)
	defer ws.Close()
	sendEngineRename(t, ws, "wx", "New Name")
	msg, scope := decodeScopedError(t, nextFrameOfType(t, ws, "error"))
	if scope != "engine_rename" {
		t.Fatalf("scope = %q, want engine_rename", scope)
	}
	if msg != "engine \"wechat-1\" is driven by an external connector and cannot be renamed" {
		t.Fatalf("message = %q", msg)
	}
	if mgr.Get("wx").Name != "wechat-1" {
		t.Fatal("read-only engine name must survive a rename request")
	}
}

func TestEngineRename_Normal_RenamesPersistsAndListsReceipt(t *testing.T) {
	c, _, mgr, _ := deleteTestRig(t)
	ws := dialAndStore(t, c)
	defer ws.Close()
	// Padded input proves the trim: the stored and echoed name is bare.
	sendEngineRename(t, ws, "e2", "  Renamed  ")

	raw := nextFrameOfType(t, ws, "engine_list")
	var list struct {
		Engines []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"engines"`
		ActiveID string `json:"activeID"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("unmarshal engine_list: %v", err)
	}
	byID := map[string]string{}
	for _, e := range list.Engines {
		byID[e.ID] = e.Name
	}
	if byID["e2"] != "Renamed" {
		t.Fatalf("engine_list e2 name = %q, want Renamed (trimmed)", byID["e2"])
	}
	if byID["main"] != "main" {
		t.Fatalf("engine_list main name = %q, want untouched", byID["main"])
	}
	if list.ActiveID != "main" {
		t.Fatalf("activeID = %q, rename must not switch engines", list.ActiveID)
	}
	if mgr.Get("e2").Name != "Renamed" {
		t.Fatalf("mgr e2 name = %q, want Renamed", mgr.Get("e2").Name)
	}

	// The restart oracle: parse meta.json back the way boot does and check
	// every engine's name — the rename must survive, the old name must not.
	dir := c.mock().ProjectDir()
	meta, err := short.ReadWorkspaceMeta(dir)
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	names := map[string]string{}
	for _, e := range meta.Engines {
		names[e.ID] = e.Name
	}
	if names["e2"] != "Renamed" {
		t.Fatalf("meta.json e2 name = %q, want Renamed (restart would lose it)", names["e2"])
	}
	if names["main"] != "main" {
		t.Fatalf("meta.json main name = %q, want untouched", names["main"])
	}
}

func TestEngineRename_NilManagerRejected(t *testing.T) {
	c, _ := bareConnectorA3(t, nil)
	c.handleEngineRename("e2", "x")
	frames := takeAllFramesA3(c)
	if len(frames) != 1 || !strings.Contains(frames[0], "engine manager unavailable") {
		t.Fatalf("frames = %v, want manager-unavailable scoped error", frames)
	}
	if !strings.Contains(frames[0], `"scope":"engine_rename"`) {
		t.Fatalf("frame lacks engine_rename scope: %s", frames[0])
	}
}

func TestEngineRename_PersistMetaFailureStillRenames(t *testing.T) {
	c, _, mgr, _ := deleteTestRig(t)
	// A FILE where the project dir should be: WriteWorkspaceMeta must fail,
	// the handler warns and still answers with the list.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	e2Mock, _ := c.slots["e2"].engine.(*mockEngine)
	e2Mock.projectDirFn = func() string { return blocker }

	sink := a1SwapDefaultLogger(t)
	c.handleEngineRename("e2", "Renamed")
	if mgr.Get("e2").Name != "Renamed" {
		t.Fatal("rename must survive a PersistMeta failure")
	}
	if sink.count("wui:engine_rename:PersistMeta failed") == 0 {
		t.Fatal("PersistMeta failure must be warned")
	}
	frames := takeAllFramesA3(c)
	if len(frames) != 1 || !strings.Contains(frames[0], "Renamed") {
		t.Fatalf("frames = %v, want the engine_list push to survive the persist failure", frames)
	}
}

func TestEngineRename_EmptyIDEIsIgnoredByRouting(t *testing.T) {
	c, _, _, _ := deleteTestRig(t)
	ws := dialAndStore(t, c)
	defer ws.Close()
	emptyMsg, _ := json.Marshal(map[string]string{"type": "engine_rename", "engineID": "", "name": "x"})
	if err := ws.WriteMessage(websocket.TextMessage, emptyMsg); err != nil {
		t.Fatal(err)
	}
	// The empty-id frame must be dropped by routing; the next rename's
	// error is the FIRST frame the client sees.
	sendEngineRename(t, ws, "ghost", "x")
	msg, _ := decodeScopedError(t, nextFrameOfType(t, ws, "error"))
	if msg != "unknown engine: ghost" {
		t.Fatalf("first error frame = %q, want the ghost one (empty id was routed)", msg)
	}
}

func TestEngineRename_EngineWithoutSlot_PersistsViaViewEngine(t *testing.T) {
	dir := t.TempDir()
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "main"})
	// No slot for "slotless": the view state carries the real engine, so the
	// dir fallback must find it — a no-op persist would silently drop the
	// rename and restart would resurrect the old name.
	store, err := short.NewStore(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	slotless := engine.New(&engine.Params{EngineID: "slotless", Model: "m"})
	slotless.SetDispatcher(hub.NewHub())
	slotless.SetStore(store, dir)
	mgr.Add(&engine.EngineViewState{ID: "slotless", Name: "slotless", Engine: slotless})
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	c.mgr = mgr
	c.handleEngineRename("slotless", "Renamed")
	frames := takeAllFramesA3(c)
	if len(frames) != 1 || !strings.Contains(frames[0], "Renamed") {
		t.Fatalf("frames = %v, want a single engine_list carrying the new name", frames)
	}
	meta, err := short.ReadWorkspaceMeta(dir)
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	names := map[string]string{}
	for _, e := range meta.Engines {
		names[e.ID] = e.Name
	}
	if names["slotless"] != "Renamed" {
		t.Fatalf("meta.json slotless name = %q, want Renamed (rename lost on restart)", names["slotless"])
	}
}

func TestEngineRename_SlotWithNilEngine_FallsBackToViewEngine(t *testing.T) {
	dir := t.TempDir()
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "main"})
	store, err := short.NewStore(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	e2 := engine.New(&engine.Params{EngineID: "e2"})
	e2.SetDispatcher(hub.NewHub())
	e2.SetStore(store, dir)
	mgr.Add(&engine.EngineViewState{ID: "e2", Name: "agent-2", Engine: e2})
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	c.mgr = mgr
	// A slot whose engine is gone: the && guard must skip it (a sloppy ||
	// would deref the nil engine) and the vs.Engine fallback must locate
	// the project dir for the persist.
	c.slots["e2"] = &engineSlot{
		engineID:    "e2",
		engine:      nil,
		hub:         h,
		taskToolIDs: make(map[string]bool),
	}
	c.handleEngineRename("e2", "Renamed")
	if got := mgr.Get("e2").Name; got != "Renamed" {
		t.Fatalf("e2 name = %q, want Renamed", got)
	}
	meta, err := short.ReadWorkspaceMeta(dir)
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	names := map[string]string{}
	for _, e := range meta.Engines {
		names[e.ID] = e.Name
	}
	if names["e2"] != "Renamed" {
		t.Fatalf("meta.json e2 name = %q, want Renamed via the view-engine dir", names["e2"])
	}
}
