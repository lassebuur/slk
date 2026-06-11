// internal/ui/winmodels.go
//
// Per-window messages.Model store (window-management design §2,
// Phase 3). Each window in the tree owns a live model; a.messagepane
// always aliases the focused window's model so the ~100 existing
// focused-window call sites keep their semantics.
package ui

import (
	"github.com/gammons/slk/internal/ui/messages"
	"github.com/gammons/slk/internal/ui/wintree"
)

// newWindowModel constructs a messages model for a new window and
// applies the app-global config every pane needs — mirroring exactly
// the Set* calls made on the root model via App's Set* forwarders
// (SetAvatarFunc / SetUserNames / SetChannels / SetEmojiContext /
// SetCustomEmoji / SetImageContext / the bootstrap spinner tick).
// The values are retained on App fields by those forwarders so
// late-created windows get them too.
func (a *App) newWindowModel(chName string) *messages.Model {
	m := messages.New(nil, chName)
	m.SetAvatarFunc(a.avatarFn)
	m.SetUserNames(a.userNames)
	m.SetChannelNames(a.channelNames)
	m.SetEmojiContext(a.emojiCtx)
	if a.emojiCustoms != nil {
		// SetCustomEmoji ran after SetEmojiContext: its customs map
		// supersedes the one captured inside emojiCtx.
		m.SetEmojiCustoms(a.emojiCustoms)
	}
	m.SetImageContext(a.imageCtx)
	m.SetSpinnerFrame(a.spinnerFrame)
	return &m
}

// modelsForChannel returns the models of every window viewing chID,
// in tree order. Used by channel-scoped event fan-out (Task 2).
func (a *App) modelsForChannel(chID string) []*messages.Model {
	if chID == "" {
		return nil
	}
	var out []*messages.Model
	for _, id := range a.wins.Leaves() {
		if ch, ok := a.wins.Channel(id); ok && ch.ID == chID {
			if m := a.winModels[id]; m != nil {
				out = append(out, m)
			}
		}
	}
	return out
}

// allWinModels returns every window's model in tree order. Used by
// workspace/global fan-out (Task 3).
func (a *App) allWinModels() []*messages.Model {
	out := make([]*messages.Model, 0, len(a.winModels))
	for _, id := range a.wins.Leaves() {
		if m := a.winModels[id]; m != nil {
			out = append(out, m)
		}
	}
	return out
}

// syncWinModels evicts models for windows no longer in the tree
// (after close/only). Additions happen explicitly in splitWindow.
func (a *App) syncWinModels() {
	live := make(map[wintree.LeafID]bool, a.wins.Len())
	for _, id := range a.wins.Leaves() {
		live[id] = true
	}
	for id := range a.winModels {
		if !live[id] {
			delete(a.winModels, id)
		}
	}
}

// resetWindowTree rebuilds the tree + model store to a single empty
// window (workspace switch).
func (a *App) resetWindowTree() {
	wins, rootWin := wintree.New(wintree.Channel{})
	a.wins = wins
	a.focusedWin = rootWin
	rootModel := a.newWindowModel("")
	a.winModels = map[wintree.LeafID]*messages.Model{rootWin: rootModel}
	a.messagepane = rootModel
}
