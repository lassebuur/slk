package ui

import (
	"testing"

	"github.com/gammons/slk/internal/ui/messages"
	"github.com/gammons/slk/internal/ui/wintree"
)

func TestWinModels_RootWindowHasModelAndPointerInvariant(t *testing.T) {
	a := newWideTestApp(t)
	if a.winModels[a.focusedWin] == nil {
		t.Fatal("root window must have a model at construction")
	}
	if a.messagepane != a.winModels[a.focusedWin] {
		t.Fatal("messagepane must point at the focused window's model")
	}
}

func TestSplitWindow_NewWindowGetsSeededClone(t *testing.T) {
	a := newWideTestApp(t)
	_, _ = a.Update(ChannelSelectedMsg{ID: "C1", Name: "general", Type: "channel"})
	a.messagepane.SetMessages([]messages.MessageItem{
		{TS: "1.0", UserName: "alice", UserID: "U1", Text: "hello", Timestamp: "1:00 PM"},
	})
	src := a.messagepane
	_ = a.splitWindow(wintree.SplitSideBySide)
	if a.messagepane == src {
		t.Fatal("focused model must be the NEW window's model after split")
	}
	if got := len(a.messagepane.Messages()); got != 1 {
		t.Fatalf("new window should be seeded with the source's messages, got %d", got)
	}
	if a.winModels[a.focusedWin] != a.messagepane {
		t.Fatal("pointer invariant broken after split")
	}
}

func TestFocusWindow_IsPointerSwapNoDispatch(t *testing.T) {
	a := newWideTestApp(t)
	_, _ = a.Update(ChannelSelectedMsg{ID: "C1", Name: "general", Type: "channel"})
	first := a.focusedWin
	_ = a.splitWindow(wintree.SplitSideBySide)
	_, _ = a.Update(ChannelSelectedMsg{ID: "C2", Name: "ops", Type: "channel"})
	// Focus back: NO ChannelSelectedMsg dispatch (per-window models),
	// but active-channel context retargets to the window's channel.
	cmd := a.focusWindow(first)
	if cmd != nil {
		t.Fatal("focusWindow must not dispatch channel selection in Phase 3")
	}
	if a.activeChannelID != "C1" {
		t.Fatalf("activeChannelID = %q, want C1 (focused window's channel)", a.activeChannelID)
	}
	if a.messagepane != a.winModels[first] {
		t.Fatal("messagepane must follow focus")
	}
}

func TestCloseWindow_EvictsModel(t *testing.T) {
	a := newWideTestApp(t)
	_ = a.splitWindow(wintree.SplitSideBySide)
	closed := a.focusedWin
	_ = a.closeWindow()
	if _, ok := a.winModels[closed]; ok {
		t.Fatal("closed window's model must be evicted")
	}
	if len(a.winModels) != a.wins.Len() {
		t.Fatalf("winModels len %d != tree len %d", len(a.winModels), a.wins.Len())
	}
}

func TestOnlyWindow_EvictsOthers(t *testing.T) {
	a := newWideTestApp(t)
	_ = a.splitWindow(wintree.SplitSideBySide)
	_ = a.splitWindow(wintree.SplitStacked)
	a.onlyWindow()
	if len(a.winModels) != 1 {
		t.Fatalf("winModels len = %d, want 1 after :only", len(a.winModels))
	}
	if a.winModels[a.focusedWin] != a.messagepane {
		t.Fatal("pointer invariant broken after :only")
	}
}

func TestWorkspaceSwitch_RebuildsModels(t *testing.T) {
	a := newWideTestApp(t)
	_ = a.splitWindow(wintree.SplitSideBySide)
	_, _ = a.Update(WorkspaceSwitchedMsg{TeamID: "T2", TeamName: "Other", Channels: nil})
	if len(a.winModels) != 1 {
		t.Fatalf("winModels len = %d, want 1 after workspace switch", len(a.winModels))
	}
	if a.messagepane == nil || a.messagepane != a.winModels[a.focusedWin] {
		t.Fatal("pointer invariant broken after workspace switch")
	}
}
