package app

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/message"
)

// sendTo drives one Message-tool call and returns the delivered-to payload.
func sendTo(t *testing.T, msg *message.MessageTool, to string) (string, error) {
	t.Helper()
	if msg == nil {
		t.Fatal("message tool missing from ToolRefs")
	}
	res, err := msg.Call(context.Background(),
		json.RawMessage(`{"to":"`+to+`","message":"ping"}`), &tool.ToolUseContext{})
	if err != nil {
		return "", err
	}
	mr, ok := res.Data.(message.MessageResult)
	if !ok {
		return "", fmt.Errorf("result data %T, want message.MessageResult", res.Data)
	}
	return "Delivered to " + mr.To, nil
}

// TestStart_MessageResolverSharesEngineMgr pins that the boot toolset and the
// production engine factory both build their message resolver against the
// process-global engine manager exposed on the Instance: an engine registered
// there must be addressable from either toolset.
func TestStart_MessageResolverSharesEngineMgr(t *testing.T) {
	t.Setenv("HOME", reloadHome(t, "32k"))
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:0")
	startInTempProject(t)
	inst, err := Start(Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		inst.LSPReg.Shutdown(ctx)
	})

	peer := engine.New(&engine.Params{Provider: inst.Provider, Model: inst.Model, EngineID: "peer-1"})
	t.Cleanup(func() { peer.Close() })
	inst.EngineMgr.Add(&engine.EngineViewState{Engine: peer, ID: "peer-1"})

	got, err := sendTo(t, inst.MainRefs.Message, "peer-1")
	if err != nil {
		t.Fatalf("boot engine to peer-1: %v — the boot SharedDeps must carry Instance.EngineMgr", err)
	}
	if got != "Delivered to peer-1" {
		t.Errorf("boot engine send result = %q, want %q", got, "Delivered to peer-1")
	}

	factoryEng, _, err := inst.EngineFactory("eng-msg", "Msg", "t", "m")
	if err != nil {
		t.Fatalf("engineFactory: %v", err)
	}
	t.Cleanup(func() { factoryEng.Close() })

	got, err = sendTo(t, factoryEng.ToolRefs().Message, "peer-1")
	if err != nil {
		t.Fatalf("factory engine to peer-1: %v — the factory's SharedDeps must carry Instance.EngineMgr", err)
	}
	if got != "Delivered to peer-1" {
		t.Errorf("factory engine send result = %q, want %q", got, "Delivered to peer-1")
	}

	dreamVS := inst.EngineMgr.Get("dream")
	if dreamVS == nil || dreamVS.Engine == nil {
		t.Fatal("dream engine missing from the manager — the boot must register it")
	}
	got, err = sendTo(t, dreamVS.Engine.ToolRefs().Message, "peer-1")
	if err != nil {
		t.Fatalf("dream engine to peer-1: %v — the dream SharedDeps must carry Instance.EngineMgr", err)
	}
	if got != "Delivered to peer-1" {
		t.Errorf("dream engine send result = %q, want %q", got, "Delivered to peer-1")
	}
}
