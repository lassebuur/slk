package searchresults

import (
	"strings"
	"testing"
)

func items() []Item {
	return []Item{
		{ChannelID: "C1", ChannelName: "general", UserName: "grant", TS: "1.0", Text: "deploy fine"},
		{ChannelID: "C2", ChannelName: "ops", UserName: "sam", TS: "2.0", Text: "deploy bad", ThreadTS: "1.5"},
	}
}

func TestOpenStartsAtInput(t *testing.T) {
	m := New()
	m.Open()
	if !m.IsVisible() || m.Query() != "" {
		t.Fatal("open state wrong")
	}
}

func TestTypingAndSubmit(t *testing.T) {
	m := New()
	m.Open()
	for _, r := range "deploy" {
		m.HandleKey(string(r))
	}
	if m.Query() != "deploy" {
		t.Fatalf("query = %q", m.Query())
	}
	if act := m.HandleKey("enter"); act != ActionSubmit {
		t.Fatalf("enter on query = %v, want ActionSubmit", act)
	}
	if !m.Loading() {
		t.Fatal("not in loading state after submit")
	}
}

func TestEmptyQuerySubmitIsNoop(t *testing.T) {
	m := New()
	m.Open()
	if act := m.HandleKey("enter"); act != ActionNone {
		t.Fatalf("enter on empty query = %v", act)
	}
}

func TestResultsNavigationAndSelect(t *testing.T) {
	m := New()
	m.Open()
	for _, r := range "deploy" {
		m.HandleKey(string(r))
	}
	m.HandleKey("enter")
	m.SetResults(items(), 2)
	if m.Loading() {
		t.Fatal("still loading after SetResults")
	}

	m.HandleKey("down")
	if act := m.HandleKey("enter"); act != ActionSelect {
		t.Fatalf("enter on result = %v, want ActionSelect", act)
	}
	sel, ok := m.Selected()
	if !ok || sel.ChannelID != "C2" || sel.ThreadTS != "1.5" {
		t.Fatalf("selected = %+v ok=%v", sel, ok)
	}
}

func TestErrorStateKeepsQuery(t *testing.T) {
	m := New()
	m.Open()
	for _, r := range "x" {
		m.HandleKey(string(r))
	}
	m.HandleKey("enter")
	m.SetError("rate limited")
	if m.Query() != "x" {
		t.Fatal("query lost on error")
	}
	// retry works
	if act := m.HandleKey("enter"); act != ActionSubmit {
		t.Fatalf("retry = %v", act)
	}
}

func TestEscCloses(t *testing.T) {
	m := New()
	m.Open()
	if act := m.HandleKey("esc"); act != ActionClose {
		t.Fatalf("esc = %v", act)
	}
	if m.IsVisible() {
		t.Fatal("still visible")
	}
}

func TestNewQueryTypingReplacesResults(t *testing.T) {
	m := New()
	m.Open()
	m.HandleKey("d")
	m.HandleKey("enter")
	m.SetResults(items(), 2)
	m.HandleKey("x") // typing returns focus to the input
	if m.Query() != "dx" {
		t.Fatalf("query = %q", m.Query())
	}
}

func TestSpaceKeyAppendsSpace(t *testing.T) {
	// bubbletea v2's Key.String() renders a literal space as "space";
	// multi-term queries must map it back to ' '.
	m := New()
	m.Open()
	m.HandleKey("a")
	m.HandleKey("space")
	m.HandleKey("b")
	if m.Query() != "a b" {
		t.Fatalf("query = %q", m.Query())
	}
}

func TestViewSmoke(t *testing.T) {
	m := New()
	if got := m.View(80); got != "" {
		t.Fatalf("hidden View = %q, want empty", got)
	}
	m.Open()
	for _, r := range "deploy" {
		m.HandleKey(string(r))
	}
	out := m.View(80)
	if out == "" || !strings.Contains(out, "deploy") {
		t.Fatal("View must render the query")
	}

	m.HandleKey("enter")
	if out := m.View(80); !strings.Contains(out, "Searching") {
		t.Fatal("loading View must show spinner line")
	}

	m.SetResults(items(), 5)
	out = m.View(80)
	if !strings.Contains(out, "general") || !strings.Contains(out, "grant") {
		t.Fatal("results View must show channel and author")
	}
	if !strings.Contains(out, "showing 2 of 5") {
		t.Fatal("results View must show footer when total > len(items)")
	}

	m.SetError("rate limited")
	if out := m.View(80); !strings.Contains(out, "rate limited") {
		t.Fatal("error View must show error message")
	}
}
