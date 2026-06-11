# Window Management Phase 2: Window Tree + Multi-Window Rendering — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A vim-style window tree subdividing the messages region: `:sp`/`:vsp`/`ctrl+w s|v` create splits, `ctrl+w h/j/k/l/w` move focus, `:q`/`:only`/`ctrl+w q|c|o` close. The focused window is the live messages pane; unfocused windows render static placeholders (per-window live models are Phase 3).

**Architecture:** A new pure-data package `internal/ui/wintree` owns the split tree and all geometry (rects, min-size refusal, geometric navigation) with zero UI dependencies. `App` gains `wins *wintree.Tree` + `focusedWin` and thin command methods that bridge tree ops to toasts/focus/channel-selection. Rendering: when the tree has one window the existing `renderMessagesRegion` path runs untouched (zero perf change); with more windows, a recursive compositor renders the focused window through the existing (cached) messages renderer at its rect size and cheap placeholder panels elsewhere. Window-focus changes to a window on a different channel dispatch the standard `ChannelSelectedMsg` so the single live model follows focus.

**Tech Stack:** Go 1.26, bubbletea v2, lipgloss v2. Plain `go test`.

**Spec:** `docs/superpowers/specs/2026-06-11-window-management-design.md` (Design §1, §4-6; Phasing item 2). **Base branch:** create `window-management-phase2` off `window-management-phase1` (PR #81) — Phase 1's command registry is a dependency.

---

## Context for the implementer

- **Phase 1 (already on the base branch):** `:` command registry in `internal/ui/command.go` (`commands` map, `commandFunc(a *App, args []string) tea.Cmd`, `toastWithClear` for errors); command-mode prompt; `ctrl+w` is currently a no-op in normal mode (falls into `handleNormalMode`'s default arm).
- **Layout:** `panelLayout.Compute` (internal/ui/panellayout.go:83) resolves the messages region: width = `frame.MsgWidth + frame.MsgBorder` cols, height = `frame.ContentHeight` rows. The region renders as ONE string via `renderMessagesRegion(frame, themeVer, previewActive)` (internal/ui/view_messages.go:61), appended to the panels list in `App.View` (internal/ui/app.go:2391).
- **Render contract:** every panel string is exactly ContentHeight rows; `exactSize(s, w, h)` (view_helpers.go) pads/clamps. `styles.UnfocusedBorder`/`styles.FocusedBorder`/`styles.TextMuted` exist.
- **Channel switching:** emitting `ChannelSelectedMsg{ID, Name, Type}` as a `tea.Cmd` is the universal mechanism (sidebar enter app.go:1325, channel finder, links). The reducer applies it and calls `a.statusbar.SetChannel(m.Name)` (reducer_channels.go:272); a second apply-site helper exists around app.go:2178.
- **Normal-mode keys:** `handleNormalMode` (internal/ui/mode_normal.go) starts with reaction-nav sub-state intercepts — "sub-state intercepted FIRST" is the established pattern the `ctrl+w` pending state follows.
- **Tests:** `tea.KeyPressMsg{Code: 'x', Text: "x"}` / `{Code: 'w', Mod: tea.ModCtrl}`; `NewApp()` builds a test App; run from repo root: `go test ./internal/ui/...`.
- **Vim semantics chosen:** new window opens below (`:sp`) / right (`:vsp`) and takes focus (splitbelow/splitright convention). Nested same-direction splits after a collapse are NOT re-merged (rect math and navigation stay correct; vim-style normalization is YAGNI).

---

### Task 1: wintree core — tree, channels, geometry

**Files:**
- Create: `internal/ui/wintree/wintree.go`
- Create: `internal/ui/wintree/layout.go`
- Test: `internal/ui/wintree/wintree_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/ui/wintree/wintree_test.go`:

```go
package wintree

import (
	"reflect"
	"testing"
)

func TestNew_SingleLeaf(t *testing.T) {
	tr, id := New(Channel{ID: "C1", Name: "general", Type: "channel"})
	if got := tr.Leaves(); !reflect.DeepEqual(got, []LeafID{id}) {
		t.Fatalf("Leaves() = %v, want [%v]", got, id)
	}
	if tr.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", tr.Len())
	}
	ch, ok := tr.Channel(id)
	if !ok || ch.Name != "general" {
		t.Fatalf("Channel(%v) = %+v, %v", id, ch, ok)
	}
}

func TestSetChannel(t *testing.T) {
	tr, id := New(Channel{ID: "C1", Name: "general"})
	if !tr.SetChannel(id, Channel{ID: "C2", Name: "ops"}) {
		t.Fatal("SetChannel returned false for existing leaf")
	}
	if ch, _ := tr.Channel(id); ch.ID != "C2" {
		t.Fatalf("channel = %+v, want C2", ch)
	}
	if tr.SetChannel(LeafID(999), Channel{}) {
		t.Fatal("SetChannel should return false for unknown leaf")
	}
}

func TestComputeRects_SingleWindowFillsBounds(t *testing.T) {
	tr, id := New(Channel{})
	bounds := Rect{X: 0, Y: 0, W: 120, H: 40}
	rects := tr.ComputeRects(bounds)
	if rects[id] != bounds {
		t.Fatalf("rect = %+v, want %+v", rects[id], bounds)
	}
}

```

(Tests exercising `Split` — tiling, layout shape — arrive in Task 2 with the operations themselves; this task's tests only cover the single-leaf tree.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/ui/wintree/ -v`
Expected: compile error — package does not exist.

- [ ] **Step 3: Implement the core**

Create `internal/ui/wintree/wintree.go`:

```go
// Package wintree owns the vim-style window split tree for the
// messages region (window-management design §1). Pure data + geometry:
// no UI dependencies. Internal nodes are splits (direction + children,
// always divided equally in Phase 2); leaves are windows identified by
// a stable LeafID and carrying the channel they view.
package wintree

import "errors"

// Dir is a split direction, named by visual result to avoid vim's
// confusing horizontal/vertical terminology.
type Dir int

const (
	// SplitStacked is vim's :sp — children stack top-to-bottom.
	SplitStacked Dir = iota
	// SplitSideBySide is vim's :vsp — children sit left-to-right.
	SplitSideBySide
)

// NavDir is a geometric focus-navigation direction (ctrl+w h/j/k/l).
type NavDir int

const (
	NavLeft NavDir = iota
	NavDown
	NavUp
	NavRight
)

// LeafID identifies a window. IDs are stable for the window's
// lifetime and never reused within a Tree.
type LeafID int

// Channel is the channel a window views. Mirrors the fields of
// ui.ChannelSelectedMsg so focus changes can re-dispatch selection.
type Channel struct {
	ID   string
	Name string
	Type string
}

// Rect is a window rectangle in screen cells, including the window's
// border rows/cols. Rects produced by ComputeRects tile the bounds
// exactly.
type Rect struct {
	X, Y, W, H int
}

// Minimum window rect sizes, border inclusive. MinWidth matches the
// messages pane's 40-col content minimum plus its 2 border cols.
const (
	MinWidth  = 42
	MinHeight = 8
)

var (
	ErrNotFound   = errors.New("wintree: no such window")
	ErrNoRoom     = errors.New("wintree: not enough room")
	ErrLastWindow = errors.New("wintree: cannot close last window")
)

// node is either a leaf (len(children) == 0; id/ch valid) or a split
// (dir/children valid).
type node struct {
	id       LeafID
	ch       Channel
	dir      Dir
	children []*node
}

func (n *node) isLeaf() bool { return len(n.children) == 0 }

// Tree is the window tree. Zero value is not usable; construct with New.
type Tree struct {
	root *node
	next LeafID
}

// New returns a tree with a single window viewing ch, and that
// window's id.
func New(ch Channel) (*Tree, LeafID) {
	t := &Tree{next: 2}
	t.root = &node{id: 1, ch: ch}
	return t, 1
}

// Len returns the number of windows.
func (t *Tree) Len() int { return len(t.Leaves()) }

// Leaves returns all window ids in tree (depth-first, left-to-right /
// top-to-bottom) order.
func (t *Tree) Leaves() []LeafID {
	var out []LeafID
	var walk func(n *node)
	walk = func(n *node) {
		if n.isLeaf() {
			out = append(out, n.id)
			return
		}
		for _, c := range n.children {
			walk(c)
		}
	}
	walk(t.root)
	return out
}

// findLeaf returns the leaf with the given id and its parent split
// (parent == nil when the leaf is the root). nil leaf means not found.
func (t *Tree) findLeaf(id LeafID) (leaf, parent *node) {
	var walk func(n, p *node) (*node, *node)
	walk = func(n, p *node) (*node, *node) {
		if n.isLeaf() {
			if n.id == id {
				return n, p
			}
			return nil, nil
		}
		for _, c := range n.children {
			if l, lp := walk(c, n); l != nil {
				return l, lp
			}
		}
		return nil, nil
	}
	return walk(t.root, nil)
}

// Channel returns the channel of the given window.
func (t *Tree) Channel(id LeafID) (Channel, bool) {
	l, _ := t.findLeaf(id)
	if l == nil {
		return Channel{}, false
	}
	return l.ch, true
}

// SetChannel updates the channel of the given window. Returns false
// if the window does not exist.
func (t *Tree) SetChannel(id LeafID, ch Channel) bool {
	l, _ := t.findLeaf(id)
	if l == nil {
		return false
	}
	l.ch = ch
	return true
}

// firstLeaf returns the first (tree-order) leaf under n.
func firstLeaf(n *node) *node {
	for !n.isLeaf() {
		n = n.children[0]
	}
	return n
}
```

Create `internal/ui/wintree/layout.go`:

```go
package wintree

// LayoutNode is the renderable shape of the tree: leaves carry their
// window id and rect; splits carry direction and children. The UI
// walks this to compose window panes (it never sees *node).
type LayoutNode struct {
	Leaf     bool
	ID       LeafID
	Rect     Rect
	Dir      Dir
	Children []LayoutNode
}

// Layout resolves every node's rect within bounds. Children of a
// split divide the parent extent equally; remainders go to the
// earliest children one cell each, so rects always tile exactly.
func (t *Tree) Layout(bounds Rect) LayoutNode {
	return layoutNode(t.root, bounds)
}

func layoutNode(n *node, r Rect) LayoutNode {
	if n.isLeaf() {
		return LayoutNode{Leaf: true, ID: n.id, Rect: r}
	}
	out := LayoutNode{Rect: r, Dir: n.dir, Children: make([]LayoutNode, 0, len(n.children))}
	k := len(n.children)
	if n.dir == SplitSideBySide {
		base, rem := r.W/k, r.W%k
		x := r.X
		for i, c := range n.children {
			w := base
			if i < rem {
				w++
			}
			out.Children = append(out.Children, layoutNode(c, Rect{X: x, Y: r.Y, W: w, H: r.H}))
			x += w
		}
	} else {
		base, rem := r.H/k, r.H%k
		y := r.Y
		for i, c := range n.children {
			h := base
			if i < rem {
				h++
			}
			out.Children = append(out.Children, layoutNode(c, Rect{X: r.X, Y: y, W: r.W, H: h}))
			y += h
		}
	}
	return out
}

// ComputeRects flattens Layout into a per-window rect map.
func (t *Tree) ComputeRects(bounds Rect) map[LeafID]Rect {
	out := make(map[LeafID]Rect)
	var walk func(LayoutNode)
	walk = func(n LayoutNode) {
		if n.Leaf {
			out[n.ID] = n.Rect
			return
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(t.Layout(bounds))
	return out
}

// nodeRect returns the rect of an internal *node within bounds
// (used by Split's refusal check). Same division as Layout.
func (t *Tree) nodeRect(target *node, bounds Rect) (Rect, bool) {
	var found Rect
	var ok bool
	var walk func(n *node, r Rect)
	walk = func(n *node, r Rect) {
		if n == target {
			found, ok = r, true
			return
		}
		if n.isLeaf() {
			return
		}
		k := len(n.children)
		if n.dir == SplitSideBySide {
			base, rem := r.W/k, r.W%k
			x := r.X
			for i, c := range n.children {
				w := base
				if i < rem {
					w++
				}
				walk(c, Rect{X: x, Y: r.Y, W: w, H: r.H})
				x += w
			}
		} else {
			base, rem := r.H/k, r.H%k
			y := r.Y
			for i, c := range n.children {
				h := base
				if i < rem {
					h++
				}
				walk(c, Rect{X: r.X, Y: y, W: r.W, H: h})
				y += h
			}
		}
	}
	walk(t.root, bounds)
	return found, ok
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/ui/wintree/ -v`
Expected: PASS (TestNew_SingleLeaf, TestSetChannel, TestComputeRects_SingleWindowFillsBounds).

- [ ] **Step 5: Commit**

```bash
git add internal/ui/wintree/
git commit -m "feat(wintree): window tree core - leaves, channels, layout geometry"
```

---

### Task 2: wintree ops — Split, Close, Only, Cycle

**Files:**
- Create: `internal/ui/wintree/ops.go`
- Test: `internal/ui/wintree/ops_test.go` (create; also move the two deferred tests from Task 1 here)

- [ ] **Step 1: Write the failing tests**

Create `internal/ui/wintree/ops_test.go`:

```go
package wintree

import (
	"errors"
	"testing"
)

var testBounds = Rect{X: 0, Y: 0, W: 180, H: 48}

func TestComputeRects_TilesExactly(t *testing.T) {
	// Build: vsplit (side-by-side), then split the right window
	// stacked. 3 windows; rects must tile bounds with no gaps or
	// overlaps and odd extents must be fully distributed.
	tr, a := New(Channel{ID: "C1"})
	bounds := Rect{X: 0, Y: 0, W: 121, H: 41} // odd on purpose
	b, err := tr.Split(a, SplitSideBySide, bounds)
	if err != nil {
		t.Fatal(err)
	}
	c, err := tr.Split(b, SplitStacked, bounds)
	if err != nil {
		t.Fatal(err)
	}
	rects := tr.ComputeRects(bounds)
	if len(rects) != 3 {
		t.Fatalf("got %d rects, want 3", len(rects))
	}
	area := 0
	for id, r := range rects {
		if r.W < MinWidth || r.H < MinHeight {
			t.Fatalf("window %v rect %+v below minimums", id, r)
		}
		area += r.W * r.H
	}
	if area != bounds.W*bounds.H {
		t.Fatalf("rect areas sum to %d, want %d (gap or overlap)", area, bounds.W*bounds.H)
	}
	// a is the left column: full height, x=0.
	if rects[a].X != 0 || rects[a].H != bounds.H {
		t.Fatalf("left window rect = %+v", rects[a])
	}
	// b above c in the right column, same x/width.
	if rects[b].X != rects[c].X || rects[b].W != rects[c].W {
		t.Fatalf("right column rects misaligned: b=%+v c=%+v", rects[b], rects[c])
	}
	if rects[b].Y+rects[b].H != rects[c].Y {
		t.Fatalf("b and c not vertically adjacent: b=%+v c=%+v", rects[b], rects[c])
	}
}

func TestLayout_TreeShapeMatchesRects(t *testing.T) {
	tr, a := New(Channel{})
	bounds := Rect{X: 0, Y: 0, W: 120, H: 40}
	b, _ := tr.Split(a, SplitSideBySide, bounds)
	layout := tr.Layout(bounds)
	if layout.Leaf {
		t.Fatal("root layout node should be a split")
	}
	if layout.Dir != SplitSideBySide || len(layout.Children) != 2 {
		t.Fatalf("layout = %+v", layout)
	}
	if !layout.Children[0].Leaf || layout.Children[0].ID != a {
		t.Fatalf("first child = %+v, want leaf %v", layout.Children[0], a)
	}
	if layout.Children[1].ID != b {
		t.Fatalf("second child = %+v, want leaf %v", layout.Children[1], b)
	}
	rects := tr.ComputeRects(bounds)
	if layout.Children[0].Rect != rects[a] || layout.Children[1].Rect != rects[b] {
		t.Fatal("Layout rects disagree with ComputeRects")
	}
}

func TestSplit_ClonesChannelAndOrdersNewWindowAfter(t *testing.T) {
	tr, a := New(Channel{ID: "C1", Name: "general", Type: "channel"})
	b, err := tr.Split(a, SplitSideBySide, testBounds)
	if err != nil {
		t.Fatal(err)
	}
	if ch, _ := tr.Channel(b); ch != (Channel{ID: "C1", Name: "general", Type: "channel"}) {
		t.Fatalf("new window channel = %+v, want clone of source", ch)
	}
	if got := tr.Leaves(); len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatalf("Leaves() = %v, want [%v %v] (new window after/right of source)", got, a, b)
	}
	rects := tr.ComputeRects(testBounds)
	if rects[b].X <= rects[a].X {
		t.Fatalf("vsp must place new window to the right: a=%+v b=%+v", rects[a], rects[b])
	}
}

func TestSplit_StackedPlacesNewWindowBelow(t *testing.T) {
	tr, a := New(Channel{ID: "C1"})
	b, err := tr.Split(a, SplitStacked, testBounds)
	if err != nil {
		t.Fatal(err)
	}
	rects := tr.ComputeRects(testBounds)
	if rects[b].Y <= rects[a].Y {
		t.Fatalf("sp must place new window below: a=%+v b=%+v", rects[a], rects[b])
	}
}

func TestSplit_SameDirInsertsSiblingNotNested(t *testing.T) {
	tr, a := New(Channel{})
	b, _ := tr.Split(a, SplitSideBySide, testBounds)
	c, err := tr.Split(b, SplitSideBySide, Rect{X: 0, Y: 0, W: 300, H: 48})
	if err != nil {
		t.Fatal(err)
	}
	layout := tr.Layout(Rect{X: 0, Y: 0, W: 300, H: 48})
	if len(layout.Children) != 3 {
		t.Fatalf("same-dir split should produce 3 siblings, got layout %+v", layout)
	}
	if got := tr.Leaves(); got[0] != a || got[1] != b || got[2] != c {
		t.Fatalf("Leaves() = %v, want [a b c] order", got)
	}
}

func TestSplit_RefusesWhenNoRoom(t *testing.T) {
	tr, a := New(Channel{})
	// Bounds too narrow for two side-by-side windows.
	narrow := Rect{X: 0, Y: 0, W: 2*MinWidth - 1, H: 48}
	if _, err := tr.Split(a, SplitSideBySide, narrow); !errors.Is(err, ErrNoRoom) {
		t.Fatalf("err = %v, want ErrNoRoom", err)
	}
	// Stacked still fits in the same bounds.
	if _, err := tr.Split(a, SplitStacked, narrow); err != nil {
		t.Fatalf("stacked split should fit: %v", err)
	}
	// Unknown window.
	if _, err := tr.Split(LeafID(999), SplitStacked, testBounds); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestSplit_SameDirRefusalCountsAllSiblings(t *testing.T) {
	tr, a := New(Channel{})
	bounds := Rect{X: 0, Y: 0, W: 3*MinWidth - 1, H: 48} // room for 2, not 3
	b, err := tr.Split(a, SplitSideBySide, bounds)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Split(b, SplitSideBySide, bounds); !errors.Is(err, ErrNoRoom) {
		t.Fatalf("third column must be refused: err = %v", err)
	}
}

func TestClose_CollapsesAndReturnsNeighbor(t *testing.T) {
	tr, a := New(Channel{ID: "C1"})
	b, _ := tr.Split(a, SplitSideBySide, testBounds)
	c, _ := tr.Split(b, SplitStacked, testBounds)
	// Close c: focus falls to its sibling b; b's column re-expands.
	next, err := tr.Close(c)
	if err != nil {
		t.Fatal(err)
	}
	if next != b {
		t.Fatalf("focus candidate = %v, want %v", next, b)
	}
	rects := tr.ComputeRects(testBounds)
	if len(rects) != 2 {
		t.Fatalf("got %d windows, want 2", len(rects))
	}
	if rects[b].H != testBounds.H {
		t.Fatalf("b should re-expand to full height, got %+v", rects[b])
	}
}

func TestClose_LastWindowRefused(t *testing.T) {
	tr, a := New(Channel{})
	if _, err := tr.Close(a); !errors.Is(err, ErrLastWindow) {
		t.Fatalf("err = %v, want ErrLastWindow", err)
	}
}

func TestOnly_CollapsesToSingleWindow(t *testing.T) {
	tr, a := New(Channel{ID: "C1"})
	b, _ := tr.Split(a, SplitSideBySide, testBounds)
	_, _ = tr.Split(b, SplitStacked, testBounds)
	if err := tr.Only(b); err != nil {
		t.Fatal(err)
	}
	if got := tr.Leaves(); len(got) != 1 || got[0] != b {
		t.Fatalf("Leaves() = %v, want [%v]", got, b)
	}
	if ch, _ := tr.Channel(b); ch.ID != "C1" {
		t.Fatalf("surviving window lost its channel: %+v", ch)
	}
}

func TestCycle_WrapsInTreeOrder(t *testing.T) {
	tr, a := New(Channel{})
	b, _ := tr.Split(a, SplitSideBySide, testBounds)
	c, _ := tr.Split(b, SplitStacked, testBounds)
	if got := tr.Cycle(a, 1); got != b {
		t.Fatalf("Cycle(a) = %v, want %v", got, b)
	}
	if got := tr.Cycle(c, 1); got != a {
		t.Fatalf("Cycle(c) should wrap to %v, got %v", a, got)
	}
	if got := tr.Cycle(a, -1); got != c {
		t.Fatalf("Cycle(a, -1) should wrap to %v, got %v", c, got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/ui/wintree/ -v`
Expected: compile error — `Split`, `Close`, `Only`, `Cycle` undefined.

- [ ] **Step 3: Implement the operations**

Create `internal/ui/wintree/ops.go`:

```go
package wintree

// Split divides window id in the given direction within bounds. The
// new window clones the source's channel, is placed after (below /
// right of) the source, and its id is returned. Returns ErrNoRoom
// when the resulting windows would violate MinWidth/MinHeight at the
// current bounds, ErrNotFound for an unknown id.
func (t *Tree) Split(id LeafID, dir Dir, bounds Rect) (LeafID, error) {
	leaf, parent := t.findLeaf(id)
	if leaf == nil {
		return 0, ErrNotFound
	}

	min := MinWidth
	if dir == SplitStacked {
		min = MinHeight
	}

	// Refusal check. Inserting a sibling into a same-direction parent
	// re-divides the PARENT's extent among k+1 children; otherwise the
	// leaf's own rect divides in two.
	sameDirParent := parent != nil && parent.dir == dir
	if sameDirParent {
		pr, ok := t.nodeRect(parent, bounds)
		if !ok {
			return 0, ErrNotFound
		}
		extent := pr.W
		if dir == SplitStacked {
			extent = pr.H
		}
		if extent/(len(parent.children)+1) < min {
			return 0, ErrNoRoom
		}
	} else {
		lr, ok := t.nodeRect(leaf, bounds)
		if !ok {
			return 0, ErrNotFound
		}
		extent := lr.W
		if dir == SplitStacked {
			extent = lr.H
		}
		if extent/2 < min {
			return 0, ErrNoRoom
		}
	}

	nid := t.next
	t.next++
	newLeaf := &node{id: nid, ch: leaf.ch}

	if sameDirParent {
		idx := childIndex(parent, leaf)
		parent.children = append(parent.children, nil)
		copy(parent.children[idx+2:], parent.children[idx+1:])
		parent.children[idx+1] = newLeaf
	} else {
		// Replace the leaf in place with a split node so the parent's
		// child pointer stays valid: the old window moves into child 0.
		old := &node{id: leaf.id, ch: leaf.ch}
		leaf.id = 0
		leaf.ch = Channel{}
		leaf.dir = dir
		leaf.children = []*node{old, newLeaf}
	}
	return nid, nil
}

// Close removes window id, hands its space to its siblings, and
// returns the window that should receive focus (the previous sibling
// subtree's first leaf, or the new first sibling's). Returns
// ErrLastWindow when id is the only window.
func (t *Tree) Close(id LeafID) (LeafID, error) {
	leaf, parent := t.findLeaf(id)
	if leaf == nil {
		return 0, ErrNotFound
	}
	if parent == nil {
		return 0, ErrLastWindow
	}
	idx := childIndex(parent, leaf)
	parent.children = append(parent.children[:idx], parent.children[idx+1:]...)

	var focusNode *node
	if idx > 0 {
		focusNode = parent.children[idx-1]
	} else {
		focusNode = parent.children[0]
	}
	focusID := firstLeaf(focusNode).id

	// A split with one child dissolves: the child takes its place.
	if len(parent.children) == 1 {
		only := parent.children[0]
		parent.id = only.id
		parent.ch = only.ch
		parent.dir = only.dir
		parent.children = only.children
	}
	return focusID, nil
}

// Only collapses the tree to just window id (vim ctrl+w o / :only).
func (t *Tree) Only(id LeafID) error {
	leaf, _ := t.findLeaf(id)
	if leaf == nil {
		return ErrNotFound
	}
	t.root = &node{id: leaf.id, ch: leaf.ch}
	return nil
}

// Cycle returns the window delta steps from id in tree order,
// wrapping (ctrl+w w). Unknown ids return id unchanged.
func (t *Tree) Cycle(id LeafID, delta int) LeafID {
	ls := t.Leaves()
	for i, l := range ls {
		if l == id {
			n := len(ls)
			return ls[((i+delta)%n+n)%n]
		}
	}
	return id
}

func childIndex(parent, child *node) int {
	for i, c := range parent.children {
		if c == child {
			return i
		}
	}
	return -1
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/ui/wintree/ -v`
Expected: all PASS (Task 1's tests plus these).

- [ ] **Step 5: Commit**

```bash
git add internal/ui/wintree/
git commit -m "feat(wintree): split, close, only, cycle operations"
```

---

### Task 3: wintree geometric navigation

**Files:**
- Create: `internal/ui/wintree/navigate.go`
- Test: `internal/ui/wintree/navigate_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/ui/wintree/navigate_test.go`:

```go
package wintree

import "testing"

// grid3 builds: left column a (full height) | right column b over c.
func grid3(t *testing.T) (*Tree, LeafID, LeafID, LeafID, Rect) {
	t.Helper()
	bounds := Rect{X: 0, Y: 0, W: 180, H: 48}
	tr, a := New(Channel{})
	b, err := tr.Split(a, SplitSideBySide, bounds)
	if err != nil {
		t.Fatal(err)
	}
	c, err := tr.Split(b, SplitStacked, bounds)
	if err != nil {
		t.Fatal(err)
	}
	return tr, a, b, c, bounds
}

func TestNavigateDir_LeftRight(t *testing.T) {
	tr, a, b, c, bounds := grid3(t)
	if got, ok := tr.NavigateDir(a, NavRight, bounds); !ok || got != b {
		t.Fatalf("a right = %v %v, want %v (largest overlap: b is upper)", got, ok, b)
	}
	if got, ok := tr.NavigateDir(b, NavLeft, bounds); !ok || got != a {
		t.Fatalf("b left = %v %v, want %v", got, ok, a)
	}
	if got, ok := tr.NavigateDir(c, NavLeft, bounds); !ok || got != a {
		t.Fatalf("c left = %v %v, want %v", got, ok, a)
	}
}

func TestNavigateDir_UpDown(t *testing.T) {
	tr, _, b, c, bounds := grid3(t)
	if got, ok := tr.NavigateDir(b, NavDown, bounds); !ok || got != c {
		t.Fatalf("b down = %v %v, want %v", got, ok, c)
	}
	if got, ok := tr.NavigateDir(c, NavUp, bounds); !ok || got != b {
		t.Fatalf("c up = %v %v, want %v", got, ok, b)
	}
}

func TestNavigateDir_NoNeighbor(t *testing.T) {
	tr, a, _, _, bounds := grid3(t)
	if got, ok := tr.NavigateDir(a, NavLeft, bounds); ok || got != a {
		t.Fatalf("a left should report no neighbor, got %v %v", got, ok)
	}
	if _, ok := tr.NavigateDir(a, NavUp, bounds); ok {
		t.Fatal("a up should report no neighbor")
	}
}

func TestNavigateDir_PicksLargestOverlap(t *testing.T) {
	// left column split into two rows (a top, d bottom); right column
	// b over c. From b going left, a (top row) overlaps b's Y-range
	// more than d does.
	bounds := Rect{X: 0, Y: 0, W: 180, H: 48}
	tr, a := New(Channel{})
	b, _ := tr.Split(a, SplitSideBySide, bounds)
	c, _ := tr.Split(b, SplitStacked, bounds)
	d, _ := tr.Split(a, SplitStacked, bounds)
	_ = c
	if got, ok := tr.NavigateDir(b, NavLeft, bounds); !ok || got != a {
		t.Fatalf("b left = %v, want %v (top-left window)", got, a)
	}
	if got, ok := tr.NavigateDir(c, NavLeft, bounds); !ok || got != d {
		t.Fatalf("c left = %v, want %v (bottom-left window)", got, d)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/ui/wintree/ -run TestNavigateDir -v`
Expected: compile error — `NavigateDir` undefined.

- [ ] **Step 3: Implement navigation**

Create `internal/ui/wintree/navigate.go`:

```go
package wintree

// NavigateDir returns the window adjacent to id in the given
// direction (vim ctrl+w h/j/k/l): among windows whose rect touches
// id's rect edge-on in that direction WITH positive perpendicular
// overlap (corner-only contact does not count), the one with the
// largest overlap wins. Ties resolve to the earliest window in tree
// order (deterministic). ok=false when there is no neighbor.
func (t *Tree) NavigateDir(id LeafID, nd NavDir, bounds Rect) (LeafID, bool) {
	rects := t.ComputeRects(bounds)
	cur, ok := rects[id]
	if !ok {
		return id, false
	}
	best := id
	bestOverlap := 0 // require > 0: corner contact is not adjacency
	for _, lid := range t.Leaves() { // tree order => deterministic ties
		if lid == id {
			continue
		}
		r := rects[lid]
		var adjacent bool
		var overlap int
		switch nd {
		case NavLeft:
			adjacent = r.X+r.W == cur.X
			overlap = overlap1D(r.Y, r.H, cur.Y, cur.H)
		case NavRight:
			adjacent = cur.X+cur.W == r.X
			overlap = overlap1D(r.Y, r.H, cur.Y, cur.H)
		case NavUp:
			adjacent = r.Y+r.H == cur.Y
			overlap = overlap1D(r.X, r.W, cur.X, cur.W)
		case NavDown:
			adjacent = cur.Y+cur.H == r.Y
			overlap = overlap1D(r.X, r.W, cur.X, cur.W)
		}
		if adjacent && overlap > bestOverlap {
			best, bestOverlap = lid, overlap
		}
	}
	if best == id {
		return id, false
	}
	return best, true
}

// overlap1D returns the overlap length of [a, a+alen) and [b, b+blen),
// negative when disjoint.
func overlap1D(a, alen, b, blen int) int {
	lo := max(a, b)
	hi := min(a+alen, b+blen)
	return hi - lo
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/ui/wintree/ -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ui/wintree/
git commit -m "feat(wintree): geometric ctrl+w h/j/k/l navigation"
```

---

### Task 4: App integration — window methods, commands, channel wiring

**Files:**
- Create: `internal/ui/windows.go`
- Modify: `internal/ui/app.go` (two new fields + init in NewApp + channel-apply wiring)
- Modify: `internal/ui/command.go` (register sp/vsp/q/only/on)
- Modify: `internal/ui/reducer_channels.go` (channel-apply wiring; see Step 4)
- Test: `internal/ui/windows_test.go` (create)

- [ ] **Step 1: Write the failing tests**

Create `internal/ui/windows_test.go`:

```go
package ui

import (
	"strings"
	"testing"

	"github.com/gammons/slk/internal/ui/wintree"
)

func newWideTestApp(t *testing.T) *App {
	t.Helper()
	a := NewApp()
	a.width = 200
	a.height = 50
	return a
}

func TestSplitWindow_CreatesAndFocusesNewWindow(t *testing.T) {
	a := newWideTestApp(t)
	if cmd := a.splitWindow(wintree.SplitSideBySide); cmd != nil {
		t.Fatal("successful split should not toast")
	}
	if a.wins.Len() != 2 {
		t.Fatalf("Len = %d, want 2", a.wins.Len())
	}
	if got := a.wins.Leaves(); a.focusedWin != got[1] {
		t.Fatalf("focusedWin = %v, want new window %v", a.focusedWin, got[1])
	}
	if a.focusedPanel != PanelMessages {
		t.Fatalf("focusedPanel = %v, want PanelMessages", a.focusedPanel)
	}
}

func TestSplitWindow_NoRoomToasts(t *testing.T) {
	a := NewApp()
	a.width = 60 // messages region too narrow for two columns
	a.height = 50
	cmd := a.splitWindow(wintree.SplitSideBySide)
	if cmd == nil {
		t.Fatal("expected toast-clear cmd")
	}
	if a.wins.Len() != 1 {
		t.Fatalf("Len = %d, want 1 (split refused)", a.wins.Len())
	}
	if out := a.statusbar.View(120); !strings.Contains(out, "Not enough room") {
		t.Fatalf("expected 'Not enough room' toast:\n%s", out)
	}
}

func TestCloseWindow_LastWindowToasts(t *testing.T) {
	a := newWideTestApp(t)
	cmd := a.closeWindow()
	if cmd == nil {
		t.Fatal("expected toast-clear cmd")
	}
	if out := a.statusbar.View(120); !strings.Contains(out, "Cannot close last window") {
		t.Fatalf("expected 'Cannot close last window' toast:\n%s", out)
	}
}

func TestCloseWindow_FocusFallsToNeighbor(t *testing.T) {
	a := newWideTestApp(t)
	first := a.focusedWin
	_ = a.splitWindow(wintree.SplitSideBySide)
	_ = a.closeWindow()
	if a.wins.Len() != 1 {
		t.Fatalf("Len = %d, want 1", a.wins.Len())
	}
	if a.focusedWin != first {
		t.Fatalf("focusedWin = %v, want %v", a.focusedWin, first)
	}
}

func TestFocusWindow_DifferentChannelDispatchesSelection(t *testing.T) {
	a := newWideTestApp(t)
	// Window 1 views C1.
	_, _ = a.Update(ChannelSelectedMsg{ID: "C1", Name: "general", Type: "channel"})
	first := a.focusedWin
	// Split (clone C1), then switch the new focused window to C2.
	_ = a.splitWindow(wintree.SplitSideBySide)
	_, _ = a.Update(ChannelSelectedMsg{ID: "C2", Name: "ops", Type: "channel"})
	// Focus back to window 1: its channel (C1) differs from live (C2),
	// so a ChannelSelectedMsg cmd must be returned.
	cmd := a.focusWindow(first)
	if cmd == nil {
		t.Fatal("expected channel-selection cmd")
	}
	msg, ok := cmd().(ChannelSelectedMsg)
	if !ok || msg.ID != "C1" || msg.Name != "general" {
		t.Fatalf("cmd produced %+v, want ChannelSelectedMsg for C1", msg)
	}
}

func TestChannelSelected_UpdatesFocusedWindowChannel(t *testing.T) {
	a := newWideTestApp(t)
	_, _ = a.Update(ChannelSelectedMsg{ID: "C9", Name: "incidents", Type: "channel"})
	ch, ok := a.wins.Channel(a.focusedWin)
	if !ok || ch.ID != "C9" || ch.Name != "incidents" {
		t.Fatalf("focused window channel = %+v, want C9/incidents", ch)
	}
}

func TestCommands_SpVspQOnly(t *testing.T) {
	a := newWideTestApp(t)
	_ = executeCommand(a, "vsp")
	if a.wins.Len() != 2 {
		t.Fatalf("after :vsp Len = %d, want 2", a.wins.Len())
	}
	_ = executeCommand(a, "sp")
	if a.wins.Len() != 3 {
		t.Fatalf("after :sp Len = %d, want 3", a.wins.Len())
	}
	_ = executeCommand(a, "q")
	if a.wins.Len() != 2 {
		t.Fatalf("after :q Len = %d, want 2", a.wins.Len())
	}
	_ = executeCommand(a, "only")
	if a.wins.Len() != 1 {
		t.Fatalf("after :only Len = %d, want 1", a.wins.Len())
	}
	_ = executeCommand(a, "vsp")
	_ = executeCommand(a, "on") // alias
	if a.wins.Len() != 1 {
		t.Fatalf("after :on Len = %d, want 1", a.wins.Len())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/ui/ -run 'TestSplitWindow|TestCloseWindow|TestFocusWindow|TestChannelSelected_Updates|TestCommands_SpVspQOnly' -v`
Expected: compile error — `a.wins`, `a.splitWindow` undefined.

- [ ] **Step 3: Create the window-methods file**

Create `internal/ui/windows.go`:

```go
// internal/ui/windows.go
//
// App-side window management (window-management design §1, §4).
// Thin bridge between the wintree package and App state: tree ops,
// focus movement, status-bar toasts, and the focused-window channel
// contract. Phase 2: ONE live messages model — the focused window
// renders it; focusing a window on a different channel re-dispatches
// the standard ChannelSelectedMsg so the live pane follows focus.
package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/gammons/slk/internal/ui/wintree"
)

// windowBounds returns the messages-region rectangle the window tree
// subdivides. Recomputing the layout frame here is safe: Compute is
// deterministic for unchanged inputs and View re-runs it each frame.
func (a *App) windowBounds() wintree.Rect {
	frame := a.layout.Compute(a.width, a.height, a.workspaceRail.Width(), a.sidebar.Width(), a.sidebarVisible, a.threadVisible)
	return wintree.Rect{X: 0, Y: 0, W: frame.MsgWidth + frame.MsgBorder, H: frame.ContentHeight}
}

// splitWindow creates a new window (cloning the focused window's
// channel) and focuses it. Toasts "Not enough room" on refusal.
func (a *App) splitWindow(dir wintree.Dir) tea.Cmd {
	id, err := a.wins.Split(a.focusedWin, dir, a.windowBounds())
	if err != nil {
		return toastWithClear(a, "Not enough room", 2*time.Second)
	}
	a.focusedWin = id
	a.focusedPanel = PanelMessages
	return nil
}

// closeWindow closes the focused window; focus falls to its neighbor.
// Toasts "Cannot close last window" instead of ever quitting.
func (a *App) closeWindow() tea.Cmd {
	next, err := a.wins.Close(a.focusedWin)
	if err != nil {
		return toastWithClear(a, "Cannot close last window", 2*time.Second)
	}
	return a.focusWindow(next)
}

// onlyWindow closes every window except the focused one.
func (a *App) onlyWindow() {
	_ = a.wins.Only(a.focusedWin)
}

// cycleWindow focuses the next window in tree order (ctrl+w w).
func (a *App) cycleWindow() tea.Cmd {
	return a.focusWindow(a.wins.Cycle(a.focusedWin, 1))
}

// navigateWindow focuses the geometric neighbor (ctrl+w h/j/k/l).
// No neighbor is a silent no-op, like vim.
func (a *App) navigateWindow(nd wintree.NavDir) tea.Cmd {
	if id, ok := a.wins.NavigateDir(a.focusedWin, nd, a.windowBounds()); ok {
		return a.focusWindow(id)
	}
	return nil
}

// focusWindow moves window focus to id. When the target window views
// a different channel than the live model, the standard channel
// selection is dispatched so the live pane loads it (Phase 2 single-
// model semantics; Phase 3 replaces this with per-window models).
func (a *App) focusWindow(id wintree.LeafID) tea.Cmd {
	if id == a.focusedWin {
		return nil
	}
	a.focusedWin = id
	a.focusedPanel = PanelMessages
	ch, ok := a.wins.Channel(id)
	if !ok || ch.ID == "" || ch.ID == a.activeChannelID {
		return nil
	}
	return func() tea.Msg {
		return ChannelSelectedMsg{ID: ch.ID, Name: ch.Name, Type: ch.Type}
	}
}

// setFocusedWindowChannel records the applied channel selection on
// the focused window. Called from the ChannelSelectedMsg apply path.
func (a *App) setFocusedWindowChannel(id, name, chType string) {
	a.wins.SetChannel(a.focusedWin, wintree.Channel{ID: id, Name: name, Type: chType})
}
```

Add the fields to the `App` struct in `internal/ui/app.go` (in the `// State` block, after the `cmdline` field added in Phase 1):

```go
	// wins is the vim-style window tree subdividing the messages
	// region; focusedWin is the active window within it. Phase 2:
	// the focused window renders the single live messages model,
	// unfocused windows render placeholders.
	wins       *wintree.Tree
	focusedWin wintree.LeafID
```

In `NewApp` (internal/ui/app.go — alongside the `layout: newPanelLayout()` / `renderCache: newPanelRenderCache()` initialization; find the struct literal or assignment block and match its style), initialize:

```go
	wins, rootWin := wintree.New(wintree.Channel{})
	// ... assign into the App being constructed:
	//   wins: wins, focusedWin: rootWin,
	// or a.wins = wins; a.focusedWin = rootWin — match NewApp's existing style.
```

Add the import `"github.com/gammons/slk/internal/ui/wintree"` to app.go.

- [ ] **Step 4: Wire the channel-apply path**

Find every site that applies a `ChannelSelectedMsg` by calling `a.statusbar.SetChannel(...)`:
- `internal/ui/reducer_channels.go:272` (main apply path)
- `internal/ui/app.go:2181` (second apply helper — verify whether reached on selection; if it is part of the same flow, wire both)

Immediately after each `a.statusbar.SetChannel(...)` call in the ChannelSelectedMsg apply flow, add (using the msg's fields in scope at that site, e.g. `m.ID, m.Name, m.Type`):

```go
	a.setFocusedWindowChannel(m.ID, m.Name, m.Type)
```

If the app.go:2181 site receives the name only (not ID/Type), trace its caller for the full msg; wire at whichever level has all three fields. `TestChannelSelected_UpdatesFocusedWindowChannel` is the acceptance check — it must pass via `a.Update(ChannelSelectedMsg{...})`.

- [ ] **Step 5: Register the commands**

In `internal/ui/command.go`, extend the registry and add handlers:

```go
var commands = map[string]commandFunc{
	"ws":   cmdWorkspaceFinder,
	"sp":   cmdSplit,
	"vsp":  cmdVSplit,
	"q":    cmdCloseWindow,
	"only": cmdOnlyWindow,
	"on":   cmdOnlyWindow,
}

// cmdSplit / cmdVSplit create a stacked / side-by-side split of the
// focused window (window-management design §5).
func cmdSplit(a *App, _ []string) tea.Cmd  { return a.splitWindow(wintree.SplitStacked) }
func cmdVSplit(a *App, _ []string) tea.Cmd { return a.splitWindow(wintree.SplitSideBySide) }

// cmdCloseWindow closes the focused window (never quits the app).
func cmdCloseWindow(a *App, _ []string) tea.Cmd { return a.closeWindow() }

// cmdOnlyWindow closes all other windows.
func cmdOnlyWindow(a *App, _ []string) tea.Cmd {
	a.onlyWindow()
	return nil
}
```

Add the `wintree` import to command.go.

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/ui/ -run 'TestSplitWindow|TestCloseWindow|TestFocusWindow|TestChannelSelected_Updates|TestCommands_SpVspQOnly' -v`
Expected: all PASS.

- [ ] **Step 7: Full ui package**

Run: `go test ./internal/ui/... -count=1`
Expected: all PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/ui/windows.go internal/ui/windows_test.go internal/ui/app.go internal/ui/command.go internal/ui/reducer_channels.go
git commit -m "feat(ui): window tree on App - :sp/:vsp/:q/:only, focus follows channel"
```

---

### Task 5: ctrl+w pending state + chords + help entries

**Files:**
- Modify: `internal/ui/windows.go` (chord handler)
- Modify: `internal/ui/app.go` (one field)
- Modify: `internal/ui/mode_normal.go` (pending intercept + prefix case)
- Modify: `internal/ui/keys.go` (WindowPrefix binding + help-only chord entries)
- Test: `internal/ui/windows_chord_test.go` (create)

- [ ] **Step 1: Write the failing tests**

Create `internal/ui/windows_chord_test.go`:

```go
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
	// And the swallowed key must not leak into normal handling: 'z'
	// is unbound so nothing to assert beyond state above. Esc too:
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/ui/ -run TestChord -v`
Expected: compile error — `a.pendingWinCmd` undefined.

- [ ] **Step 3: Add the pending field and chord handler**

In `internal/ui/app.go`, after the `focusedWin` field:

```go
	// pendingWinCmd is true between a ctrl+w press and its chord key
	// (vim window-command prefix). Esc or an unmapped key cancels;
	// any mode change disarms (see SetMode).
	pendingWinCmd bool
```

Also in `internal/ui/app.go`, inside `SetMode` (which already clears the `:` prompt when leaving ModeCommand — Phase 1), disarm the pending prefix on ANY mode change so a global intercept (e.g. ctrl+c quit-confirm) can't strand an armed chord state:

```go
	a.pendingWinCmd = false
	a.statusbar.SetHelpHint("")
```

Place these as the first lines of `SetMode`'s body, guarded the same way the existing command-mode cleanup is if a guard exists — but unconditional is also correct here since arming happens outside SetMode. Verify the existing `SetHelpHint` users (if any besides this feature) aren't clobbered: `rg 'SetHelpHint' internal/ui --type go`. If another caller owns the hint, scope the clear to `if a.pendingWinCmd { ... }`.

In `internal/ui/windows.go`, add:

```go
// handleWindowChord consumes the key following ctrl+w (vim window
// commands, design §4). Unmapped keys — including Esc — cancel
// silently, matching vim.
func (a *App) handleWindowChord(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "s":
		return a.splitWindow(wintree.SplitStacked)
	case "v":
		return a.splitWindow(wintree.SplitSideBySide)
	case "h", "left":
		return a.navigateWindow(wintree.NavLeft)
	case "j", "down":
		return a.navigateWindow(wintree.NavDown)
	case "k", "up":
		return a.navigateWindow(wintree.NavUp)
	case "l", "right":
		return a.navigateWindow(wintree.NavRight)
	case "w", "ctrl+w":
		return a.cycleWindow()
	case "q", "c":
		return a.closeWindow()
	case "o":
		a.onlyWindow()
		return nil
	}
	return nil
}
```

In `internal/ui/mode_normal.go`, at the very top of `handleNormalMode` (BEFORE the reaction-nav intercepts — the pending prefix is the highest-priority sub-state):

```go
	// ctrl+w pending sub-state: the next key is a window command
	// (intercepted FIRST, like the reaction-nav sub-states below).
	if a.pendingWinCmd {
		a.pendingWinCmd = false
		a.statusbar.SetHelpHint("")
		return a.handleWindowChord(msg)
	}
```

And add the prefix case to the main switch (place it near the Tab/focus cases):

```go
	case key.Matches(msg, a.keys.WindowPrefix):
		a.pendingWinCmd = true
		a.statusbar.SetHelpHint("ctrl+w …")
		return nil
```

- [ ] **Step 4: Keymap + help entries**

In `internal/ui/keys.go`, add fields to `KeyMap`:

```go
	WindowPrefix  key.Binding
	WinSplit      key.Binding
	WinVSplit     key.Binding
	WinNavigate   key.Binding
	WinCycle      key.Binding
	WinClose      key.Binding
	WinOnly       key.Binding
```

And initializers in `DefaultKeyMap()` (the chord entries are keyless help-only bindings — same trick as WorkspaceFinder; the real dispatch is `handleWindowChord`):

```go
		WindowPrefix: key.NewBinding(key.WithKeys("ctrl+w"), key.WithHelp("ctrl+w", "window commands")),
		WinSplit:     key.NewBinding(key.WithHelp("ctrl+w s / :sp", "split window")),
		WinVSplit:    key.NewBinding(key.WithHelp("ctrl+w v / :vsp", "vertical split window")),
		WinNavigate:  key.NewBinding(key.WithHelp("ctrl+w h/j/k/l", "focus window in direction")),
		WinCycle:     key.NewBinding(key.WithHelp("ctrl+w w", "cycle windows")),
		WinClose:     key.NewBinding(key.WithHelp("ctrl+w q / :q", "close window")),
		WinOnly:      key.NewBinding(key.WithHelp("ctrl+w o / :only", "close other windows")),
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/ui/ -run TestChord -v`
Expected: all PASS.

- [ ] **Step 6: Full ui package (regression check — notably the Phase 1 test asserting ctrl+w stays in ModeNormal must still hold, since the prefix arms a flag without changing mode; if `TestNormalMode_CtrlWNoLongerOpensWorkspaceFinder` fails on the pending flag, update that test to also assert/disarm the new pending state rather than weakening it)**

Run: `go test ./internal/ui/... -count=1`
Expected: all PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/ui/windows.go internal/ui/windows_chord_test.go internal/ui/app.go internal/ui/mode_normal.go internal/ui/keys.go
git commit -m "feat(ui): ctrl+w window-command chords with pending-key state"
```

---

### Task 6: Multi-window rendering with placeholders

**Files:**
- Create: `internal/ui/view_windows.go`
- Modify: `internal/ui/app.go:2391` (swap the region call)
- Test: `internal/ui/view_windows_test.go` (create)

- [ ] **Step 1: Write the failing tests**

Create `internal/ui/view_windows_test.go`:

```go
package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/gammons/slk/internal/ui/wintree"
)

// renderRegion renders the messages region exactly as App.View does,
// without depending on the tea.View wrapper API.
func renderRegion(a *App) string {
	frame := a.layout.Compute(a.width, a.height, a.workspaceRail.Width(), a.sidebar.Width(), a.sidebarVisible, a.threadVisible)
	return a.renderWindowsRegion(frame, 0, false)
}

// stripANSI: if a helper already exists in the ui test package, use it
// instead — search `rg 'func stripANSI' internal/ui` first and delete
// this copy if so.
func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		switch {
		case inEsc:
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEsc = false
			}
		case r == 0x1b:
			inEsc = true
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func TestRegion_SingleWindowUnchanged(t *testing.T) {
	a := newWideTestApp(t)
	_, _ = a.Update(ChannelSelectedMsg{ID: "C1", Name: "general", Type: "channel"})
	frame := a.layout.Compute(a.width, a.height, a.workspaceRail.Width(), a.sidebar.Width(), a.sidebarVisible, a.threadVisible)
	multi := a.renderWindowsRegion(frame, 0, false)
	direct := a.renderMessagesRegion(frame, 0, false)
	if multi != direct {
		t.Fatal("single-window region must be byte-identical to the direct messages render")
	}
}

func TestRegion_SplitRendersPlaceholderWithChannelName(t *testing.T) {
	a := newWideTestApp(t)
	_, _ = a.Update(ChannelSelectedMsg{ID: "C1", Name: "general", Type: "channel"})
	_ = a.splitWindow(wintree.SplitSideBySide)
	// Focused (new) window is live; the original window is a
	// placeholder showing its channel name.
	out := stripANSI(renderRegion(a))
	if !strings.Contains(out, "# general") {
		t.Fatalf("placeholder should show channel name '# general':\n%s", out)
	}
}

func TestRegion_SplitOutputDimensionsStable(t *testing.T) {
	a := newWideTestApp(t)
	_, _ = a.Update(ChannelSelectedMsg{ID: "C1", Name: "general", Type: "channel"})
	before := renderRegion(a)
	_ = a.splitWindow(wintree.SplitSideBySide)
	after := renderRegion(a)
	if lipgloss.Height(before) != lipgloss.Height(after) {
		t.Fatalf("row count changed after split: %d -> %d", lipgloss.Height(before), lipgloss.Height(after))
	}
	if lipgloss.Width(before) != lipgloss.Width(after) {
		t.Fatalf("width changed after split: %d -> %d", lipgloss.Width(before), lipgloss.Width(after))
	}
}

func TestRegion_CloseRestoresSingleWindowPath(t *testing.T) {
	a := newWideTestApp(t)
	_, _ = a.Update(ChannelSelectedMsg{ID: "C1", Name: "general", Type: "channel"})
	_ = a.splitWindow(wintree.SplitStacked)
	_ = a.closeWindow()
	if a.wins.Len() != 1 {
		t.Fatalf("Len = %d, want 1", a.wins.Len())
	}
	frame := a.layout.Compute(a.width, a.height, a.workspaceRail.Width(), a.sidebar.Width(), a.sidebarVisible, a.threadVisible)
	multi := a.renderWindowsRegion(frame, 0, false)
	direct := a.renderMessagesRegion(frame, 0, false)
	if multi != direct {
		t.Fatal("after closing back to one window the region must take the direct single-window path")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/ui/ -run TestRegion_ -v`
Expected: compile error — `a.renderWindowsRegion` undefined. Verify before continuing.

- [ ] **Step 3: Implement the window compositor**

Create `internal/ui/view_windows.go`:

```go
// internal/ui/view_windows.go
//
// Multi-window messages-region renderer (window-management design
// §6, Phase 2). With a single window the existing renderMessagesRegion
// path runs untouched — identical output, identical caching. With
// splits, the wintree layout is walked recursively: the FOCUSED
// window renders the real (cached) messages panel sized to its rect;
// every other window renders a cheap static placeholder (dimmed
// border + channel name). Phase 3 replaces placeholders with live
// per-window models.
package ui

import (
	"charm.land/lipgloss/v2"

	"github.com/gammons/slk/internal/ui/styles"
	"github.com/gammons/slk/internal/ui/wintree"
)

// renderWindowsRegion is the messages-region entry point called from
// App.View. Preview mode and the single-window tree delegate to the
// existing path unchanged.
func (a *App) renderWindowsRegion(frame panelLayoutFrame, themeVer int64, previewActive bool) string {
	if previewActive || a.wins.Len() == 1 {
		return a.renderMessagesRegion(frame, themeVer, previewActive)
	}
	bounds := wintree.Rect{X: 0, Y: 0, W: frame.MsgWidth + frame.MsgBorder, H: frame.ContentHeight}
	return a.renderWindowNode(a.wins.Layout(bounds), frame, themeVer)
}

// renderWindowNode renders one layout-tree node to a string of
// exactly Rect.W x Rect.H cells.
func (a *App) renderWindowNode(n wintree.LayoutNode, frame panelLayoutFrame, themeVer int64) string {
	if n.Leaf {
		if n.ID == a.focusedWin {
			sub := frame
			sub.MsgWidth = n.Rect.W - 2
			sub.MsgBorder = 2
			sub.ContentHeight = n.Rect.H
			return exactSize(a.renderMessagesRegion(sub, themeVer, false), n.Rect.W, n.Rect.H)
		}
		return a.renderPlaceholderWindow(n)
	}
	parts := make([]string, 0, len(n.Children))
	for _, c := range n.Children {
		parts = append(parts, a.renderWindowNode(c, frame, themeVer))
	}
	if n.Dir == wintree.SplitSideBySide {
		return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// renderPlaceholderWindow renders an unfocused window: dimmed border,
// channel name centered. Static and cheap — no cache needed.
func (a *App) renderPlaceholderWindow(n wintree.LayoutNode) string {
	ch, _ := a.wins.Channel(n.ID)
	name := ch.Name
	if name == "" {
		name = "(no channel)"
	} else {
		name = "# " + name
	}
	label := lipgloss.NewStyle().Foreground(styles.TextMuted).Render(name)
	inner := lipgloss.Place(n.Rect.W-2, n.Rect.H-2, lipgloss.Center, lipgloss.Center, label)
	return exactSize(
		styles.UnfocusedBorder.Width(n.Rect.W-2).Render(inner),
		n.Rect.W, n.Rect.H,
	)
}
```

In `internal/ui/app.go:2391`, change:

```go
	if s := a.renderMessagesRegion(frame, themeVer, previewActive); s != "" {
```

to:

```go
	if s := a.renderWindowsRegion(frame, themeVer, previewActive); s != "" {
```

Implementation notes for this step:
- If `styles.UnfocusedBorder.Width(...)` produces an unexpected total size, mirror exactly how `renderThreadsViewPanel` (view_messages.go:118-132) builds its bordered panel — same style, same `exactSize` wrapping.
- If `exactSize`'s actual signature differs (check view_helpers.go), adapt the calls; do not write a new sizing helper.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/ui/ -run TestRegion_ -v`
Expected: all four new tests PASS.

- [ ] **Step 5: Full ui package + benchmarks compile**

Run: `go test ./internal/ui/... -count=1 && go vet ./internal/ui/...`
Expected: all PASS, vet clean. The single-window fast path means existing view tests and benchmarks must be unaffected — investigate ANY view-test regression rather than updating the test.

- [ ] **Step 6: Commit**

```bash
git add internal/ui/view_windows.go internal/ui/view_windows_test.go internal/ui/app.go
git commit -m "feat(ui): render split windows - live focused pane, placeholder siblings"
```

---

### Task 7: Final verification

**Files:** none (verification only)

- [ ] **Step 1: Full suite, vet, build**

Run: `go test ./... -count=1 && go vet ./... && go build ./cmd/slk && rm -f slk`
Expected: everything passes/clean.

- [ ] **Step 2: gofmt on touched files**

Run: `gofmt -l internal/ui/wintree/ internal/ui/windows.go internal/ui/view_windows.go internal/ui/command.go internal/ui/keys.go internal/ui/mode_normal.go`
Expected: no output. (Repo-wide `gofmt -l` has known pre-existing dirt — only the files this phase touched must be clean.)

- [ ] **Step 3: Manual smoke (requires a configured slk)**

1. `:vsp` → two side-by-side windows; right (new) window live and focused, left shows placeholder with channel name
2. `ctrl+w h` / `ctrl+w l` → focus moves; live pane follows (placeholder swaps sides); channel switches if windows view different channels (select another channel in one window first)
3. `ctrl+w s` in a window → stacked split inside that column
4. `:q` → window closes, sibling re-expands; `:q` on last window → "Cannot close last window"
5. `ctrl+w o` / `:only` → collapses to one window, normal full-size rendering
6. Shrink terminal until `:vsp` refuses → "Not enough room"
7. `?` help → lists window commands (`ctrl+w s / :sp`, etc.)
8. Sanity: scrolling, compose, thread open/close all behave normally in the focused window

- [ ] **Step 4: Report Phase 2 complete**

Phase 3 (per-window live models + event fan-out) gets its own plan once this lands.
