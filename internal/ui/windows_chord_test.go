// internal/ui/windows_chord_test.go
//
// ctrl+w window-command chord tests (window-management design §4).
// The prefix arms a pending sub-state in normal mode; the next key is
// dispatched through handleWindowChord. Unmapped keys and Esc cancel
// silently; any mode change disarms (SetMode).
package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/gammons/slk/internal/ui/wintree"
)

func pressCtrlW(a *App) {
	_ = handleNormalMode(a, tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl})
}

func press(a *App, r rune) tea.Cmd {
	return handleNormalMode(a, tea.KeyPressMsg{Code: r, Text: string(r)})
}

func TestChord_CtrlWThenVSplits(t *testing.T) {
	a := newWideTestApp(t)
	pressCtrlW(a)
	if !a.pendingWinCmd {
		t.Fatal("ctrl+w should arm the pending window-command state")
	}
	_ = press(a, 'v')
	if a.pendingWinCmd {
		t.Fatal("chord key should disarm the pending state")
	}
	if a.wins.Len() != 2 {
		t.Fatalf("Len = %d, want 2 after ctrl+w v", a.wins.Len())
	}
}

func TestChord_SplitCloseCycleOnly(t *testing.T) {
	a := newWideTestApp(t)
	pressCtrlW(a)
	_ = press(a, 's')
	if a.wins.Len() != 2 {
		t.Fatalf("ctrl+w s: Len = %d, want 2", a.wins.Len())
	}
	pressCtrlW(a)
	_ = press(a, 'w')
	first := a.wins.Leaves()[0]
	if a.focusedWin != first {
		t.Fatalf("ctrl+w w should cycle focus to %v, got %v", first, a.focusedWin)
	}
	pressCtrlW(a)
	_ = press(a, 'q')
	if a.wins.Len() != 1 {
		t.Fatalf("ctrl+w q: Len = %d, want 1", a.wins.Len())
	}
	_ = a.splitWindow(wintree.SplitStacked)
	pressCtrlW(a)
	_ = press(a, 'o')
	if a.wins.Len() != 1 {
		t.Fatalf("ctrl+w o: Len = %d, want 1", a.wins.Len())
	}
}

func TestChord_DirectionalFocus(t *testing.T) {
	a := newWideTestApp(t)
	left := a.focusedWin
	pressCtrlW(a)
	_ = press(a, 'v') // focus moves to new right-hand window
	right := a.focusedWin
	pressCtrlW(a)
	_ = press(a, 'h')
	if a.focusedWin != left {
		t.Fatalf("ctrl+w h: focused %v, want %v", a.focusedWin, left)
	}
	pressCtrlW(a)
	_ = press(a, 'l')
	if a.focusedWin != right {
		t.Fatalf("ctrl+w l: focused %v, want %v", a.focusedWin, right)
	}
}

func TestChord_UnmappedKeyCancelsSilently(t *testing.T) {
	a := newWideTestApp(t)
	pressCtrlW(a)
	_ = press(a, 'z')
	if a.pendingWinCmd {
		t.Fatal("unmapped chord key should cancel the pending state")
	}
	if a.wins.Len() != 1 {
		t.Fatalf("Len = %d, want 1 (no side effects)", a.wins.Len())
	}
	// Esc cancels too:
	pressCtrlW(a)
	_ = handleNormalMode(a, tea.KeyPressMsg{Code: tea.KeyEscape})
	if a.pendingWinCmd {
		t.Fatal("esc should cancel the pending state")
	}
	if a.wins.Len() != 1 {
		t.Fatalf("Len = %d, want 1", a.wins.Len())
	}
}

func TestChord_CtrlWCtrlWCycles(t *testing.T) {
	a := newWideTestApp(t)
	_ = a.splitWindow(wintree.SplitSideBySide) // focus on second window
	pressCtrlW(a)
	pressCtrlW(a) // vim: ctrl+w ctrl+w == ctrl+w w
	if a.focusedWin != a.wins.Leaves()[0] {
		t.Fatalf("ctrl+w ctrl+w should cycle, focused %v", a.focusedWin)
	}
	if a.pendingWinCmd {
		t.Fatal("pending state should be disarmed after the chord")
	}
}

func TestChord_ModeChangeDisarms(t *testing.T) {
	a := newWideTestApp(t)
	pressCtrlW(a)
	a.SetMode(ModeConfirm) // e.g. global ctrl+c intercept fires mid-chord
	if a.pendingWinCmd {
		t.Fatal("any mode change must disarm the pending chord state")
	}
}
