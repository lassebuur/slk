// Package searchresults is the workspace-wide message search modal
// (ctrl+f). Unlike channelfinder it does not filter locally: Enter
// submits the query to Slack's search.messages and the caller injects
// results via SetResults/SetError.
package searchresults

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/gammons/slk/internal/ui/messages"
	"github.com/gammons/slk/internal/ui/overlay"
	"github.com/gammons/slk/internal/ui/styles"
	"github.com/muesli/reflow/truncate"
)

// Item is one search hit, rendered as
// "#channel  author  time  snippet". Built from Slack's search
// response, so uncached channels/users display fine.
type Item struct {
	ChannelID   string
	ChannelName string
	UserName    string
	TS          string
	ThreadTS    string // non-empty: hit is a thread reply
	Text        string
	Timestamp   string // pre-formatted for display
}

// Action tells the mode handler what a keypress did.
type Action int

const (
	ActionNone   Action = iota
	ActionSubmit        // Enter on a non-empty query: run the search
	ActionSelect        // Enter on a result row: jump to it
	ActionClose         // Esc: modal closed
)

type state int

const (
	stateInput state = iota
	stateLoading
	stateResults
	stateError
)

// Model is the workspace search modal.
type Model struct {
	visible  bool
	query    string
	items    []Item
	selected int
	st       state
	errMsg   string
	total    int
}

// New creates a new search results modal.
func New() Model { return Model{} }

// Open shows the modal and resets state.
func (m *Model) Open() {
	m.visible = true
	m.query = ""
	m.items = nil
	m.selected = 0
	m.st = stateInput
	m.errMsg = ""
	m.total = 0
}

// Close hides the modal.
func (m *Model) Close() { m.visible = false }

// IsVisible returns whether the modal is showing.
func (m Model) IsVisible() bool { return m.visible }

// Query returns the current query text.
func (m Model) Query() string { return m.query }

// Loading reports whether a search is in flight.
func (m Model) Loading() bool { return m.st == stateLoading }

// SetResults installs server results for the in-flight query.
func (m *Model) SetResults(items []Item, total int) {
	m.items = items
	m.total = total
	m.selected = 0
	m.st = stateResults
}

// SetError shows an error line; the query is preserved for retry.
func (m *Model) SetError(msg string) {
	m.errMsg = msg
	m.st = stateError
}

// Selected returns the highlighted result.
func (m Model) Selected() (Item, bool) {
	if m.st != stateResults || m.selected < 0 || m.selected >= len(m.items) {
		return Item{}, false
	}
	return m.items[m.selected], true
}

// HandleKey processes a normalized key string ("enter", "esc", "up",
// "down", "backspace", "space", or a printable rune) and reports the
// action.
func (m *Model) HandleKey(keyStr string) Action {
	switch keyStr {
	case "esc":
		m.Close()
		return ActionClose
	case "enter":
		if m.st == stateResults {
			if _, ok := m.Selected(); ok {
				return ActionSelect
			}
			return ActionNone
		}
		if m.query == "" {
			return ActionNone
		}
		m.st = stateLoading
		return ActionSubmit
	case "up", "ctrl+k":
		if m.st == stateResults && m.selected > 0 {
			m.selected--
		}
	case "down", "ctrl+j":
		if m.st == stateResults && m.selected < len(m.items)-1 {
			m.selected++
		}
	case "backspace":
		if m.query != "" {
			r := []rune(m.query)
			m.query = string(r[:len(r)-1])
			m.st = stateInput
		}
	case "space":
		// bubbletea v2's Key.String() renders a literal space as
		// "space"; queries can be multi-term, so map it back.
		m.query += " "
		m.st = stateInput
	default:
		if len([]rune(keyStr)) == 1 {
			m.query += keyStr
			m.st = stateInput
		}
	}
	return ActionNone
}

// maxVisibleRows is the height of the scroll window for the results list.
const maxVisibleRows = 10

// boxWidth returns the modal's outer width for a given terminal width.
// Single source of truth for renderBox and BoxSize.
func boxWidth(termWidth int) int {
	w := termWidth * 2 / 3
	if w < 40 {
		w = 40
	}
	if w > 100 {
		w = 100
	}
	return w
}

// visibleWindow returns the [start, end) slice of m.items currently
// shown in the results list, applying the same scroll-window math the
// renderer uses.
func (m *Model) visibleWindow() (int, int) {
	maxVisible := maxVisibleRows
	total := len(m.items)
	if maxVisible > total {
		maxVisible = total
	}
	startIdx := 0
	if m.selected >= maxVisible {
		startIdx = m.selected - maxVisible + 1
	}
	endIdx := startIdx + maxVisible
	if endIdx > total {
		endIdx = total
		startIdx = endIdx - maxVisible
		if startIdx < 0 {
			startIdx = 0
		}
	}
	return startIdx, endIdx
}

// BoxSize returns the outer dimensions of the rendered modal box for the
// given terminal size. termHeight is unused (this modal's height depends
// only on its row count) but kept for interface symmetry with overlays
// whose height is terminal-dependent.
func (m *Model) BoxSize(termWidth, termHeight int) (int, int) {
	nRows := len(m.bodyLines(boxWidth(termWidth) - 4))
	if nRows < 1 {
		nRows = 1
	}
	// height = top border + top padding + title + input + blank + rows +
	// bottom padding + bottom border = nRows + 7.
	return boxWidth(termWidth), nRows + 7
}

// View renders just the overlay box.
func (m Model) View(termWidth int) string {
	return m.renderBox(termWidth)
}

// ViewOverlay renders the overlay as a centered modal with a dark backdrop.
func (m Model) ViewOverlay(termWidth, termHeight int, background string) string {
	if !m.visible {
		return background
	}

	box := m.renderBox(termWidth)
	if box == "" {
		return background
	}

	return overlay.DimmedOverlay(termWidth, termHeight, background, box, 0.5)
}

func (m Model) renderBox(termWidth int) string {
	if !m.visible {
		return ""
	}

	// Overlay dimensions
	overlayWidth := boxWidth(termWidth)
	innerWidth := overlayWidth - 4 // border + padding

	// All inner spans share the modal bg so the dimmed app behind the
	// overlay doesn't bleed through where individual styled fragments end.
	bg := styles.Background

	// Title
	title := lipgloss.NewStyle().
		Bold(true).
		Background(bg).
		Foreground(styles.Primary).
		Render("Search Workspace")

	// Query input with blue left border
	var inputText string
	if m.query == "" {
		placeholder := lipgloss.NewStyle().Background(bg).Foreground(styles.TextMuted).Render("Type a query, Enter to search...")
		inputText = "█ " + placeholder
	} else {
		inputText = m.query + "█"
	}
	input := lipgloss.NewStyle().
		BorderStyle(lipgloss.Border{Left: "▌"}).
		BorderLeft(true).
		BorderForeground(styles.Primary).
		BorderBackground(bg).
		PaddingLeft(1).
		Background(bg).
		Foreground(styles.TextPrimary).
		Render(inputText)

	content := title + "\n" + input + "\n\n" + strings.Join(m.bodyLines(innerWidth), "\n")

	// Re-paint modal bg+fg after every ANSI reset emitted by inner styled
	// spans so trailing cells don't inherit the dimmed app behind the
	// overlay.
	content = messages.ReapplyBgAfterResets(content, messages.BgANSI()+messages.FgANSI())

	// Wrap in a bordered box
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(styles.Primary).
		BorderBackground(bg).
		Background(bg).
		Padding(1, 1).
		Width(overlayWidth).
		Render(content)
}

// bodyLines renders the rows below the input line for the current state:
// a spinner line while loading, the error line, a "No results"
// placeholder, or the result rows plus an optional "showing K of N"
// footer. innerWidth is the usable content width inside the box.
func (m Model) bodyLines(innerWidth int) []string {
	bg := styles.Background
	muted := lipgloss.NewStyle().Background(bg).Foreground(styles.TextMuted)

	switch m.st {
	case stateLoading:
		return []string{muted.Italic(true).Render("Searching…")}
	case stateError:
		errLine := lipgloss.NewStyle().Background(bg).Foreground(styles.Error).Render(m.errMsg)
		if lipgloss.Width(errLine) > innerWidth {
			errLine = truncate.StringWithTail(errLine, uint(innerWidth), "…")
		}
		return []string{errLine}
	case stateResults:
		if len(m.items) == 0 {
			return []string{muted.Italic(true).Render("No results")}
		}
		return m.resultRows(innerWidth)
	default: // stateInput
		return []string{""}
	}
}

// resultRows renders the visible window of result rows ("#channel
// author  timestamp  snippet", selected row highlighted) plus the
// "showing K of N" footer when the server reported more matches than
// were fetched.
func (m Model) resultRows(innerWidth int) []string {
	bg := styles.Background
	contentWidth := innerWidth - 1 // 1 col indicator/space prefix

	startIdx, endIdx := m.visibleWindow()
	var rows []string
	for i := startIdx; i < endIdx; i++ {
		item := m.items[i]
		isSelected := i == m.selected

		// Render fragments separately (see channelfinder): a single
		// outer style over pre-styled text would lose attributes
		// after each inner ANSI reset.
		chanStyle := lipgloss.NewStyle().Background(bg).Foreground(styles.TextMuted)
		nameStyle := lipgloss.NewStyle().Background(bg).Foreground(styles.TextPrimary)
		textStyle := lipgloss.NewStyle().Background(bg).Foreground(styles.TextPrimary)
		if isSelected {
			nameStyle = nameStyle.Foreground(styles.Primary).Bold(true)
			textStyle = textStyle.Foreground(styles.Primary).Bold(true)
		}

		snippet := strings.ReplaceAll(item.Text, "\n", " ")
		line := chanStyle.Render("#"+item.ChannelName) + "  " +
			nameStyle.Render(item.UserName) + "  " +
			chanStyle.Render(item.Timestamp) + "  " +
			textStyle.Render(snippet)

		// Truncate to fit (truncate.StringWithTail is ANSI-aware).
		if lipgloss.Width(line) > contentWidth {
			line = truncate.StringWithTail(line, uint(contentWidth), "…")
		}
		// Right-pad with spaces to fill the row.
		if pad := contentWidth - lipgloss.Width(line); pad > 0 {
			line += strings.Repeat(" ", pad)
		}

		if isSelected {
			indicator := lipgloss.NewStyle().Background(bg).Foreground(styles.Accent).Render("▌")
			rows = append(rows, indicator+line)
		} else {
			rows = append(rows, " "+line)
		}
	}

	if m.total > len(m.items) {
		footer := lipgloss.NewStyle().Background(bg).Foreground(styles.TextMuted).
			Render(fmt.Sprintf("showing %d of %d", len(m.items), m.total))
		rows = append(rows, footer)
	}
	return rows
}
