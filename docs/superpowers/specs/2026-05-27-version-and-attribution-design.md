# Version display and author attribution

**Date:** 2026-05-27
**Status:** Draft — pending implementation plan

## Summary

Expose the slk binary's build-time version through the existing CLI version
flags and the in-app `?` help modal, and add an author/website attribution
line to both surfaces. The CLI already supports `-v`/`--version`/`version`;
this work centralizes the version metadata, adds the attribution line, and
surfaces the same information inside the TUI's Keybindings modal.

## Goals

- `slk -v`, `slk --version`, `slk version` print the version plus a new
  attribution line crediting Grant Ammons and linking to https://grant.dev.
- The `?` help modal includes a footer line above its existing controls
  footer showing `slk <version> — by Grant Ammons — https://grant.dev`.
- The website URL is rendered as an OSC 8 terminal hyperlink so it's
  clickable in supporting terminals and degrades to plain text elsewhere.
- A single source of truth holds version metadata, shared by CLI and TUI.

## Non-goals

- Changing `slk --help` (`printHelp`) output beyond what it already shows.
- Adding any other build metadata (Go version, build host, etc.).
- Adding any UI surface for the attribution other than `--version` and `?`.

## Architecture

### New package: `internal/version`

Create `internal/version/version.go` exporting build-time variables and
format helpers. This is the only place where build-time `-ldflags -X`
injection points exist after this change.

```go
package version

// Build-time vars. Defaults are used for local `go build`.
// GoReleaser injects real values via -ldflags -X.
var (
    Version = "dev"
    Commit  = "none"
    Date    = "unknown"
)

// Short returns "v1.2.3" for tagged builds, or "dev" for untagged builds.
// Prepends "v" only when Version is non-"dev" and doesn't already start
// with "v". Handles non-strict semver (e.g. "1.2.3-next") by treating it
// as opaque and just prepending "v".
func Short() string

// CLILongLine returns:
//   "slk <Short> (commit <Commit>, built <Date>)"
// Matches the existing format printed by `slk --version`.
func CLILongLine() string

// AttributionLine returns:
//   "by Grant Ammons — <osc8-wrapped https://grant.dev>"
// The URL is wrapped with OSC 8 hyperlink escape sequences so it is
// clickable in supporting terminals. Display text equals the URL itself,
// so terminals without OSC 8 support render plain "https://grant.dev".
func AttributionLine() string

// ModalFooter returns:
//   "slk <Short> — <AttributionLine>"
// Single-line attribution string suitable for the help modal footer.
func ModalFooter() string
```

OSC 8 wire format used by `AttributionLine`:

```
\x1b]8;;https://grant.dev\x1b\\https://grant.dev\x1b]8;;\x1b\\
```

The visible text equals the URL target so non-OSC-8 terminals show the URL
as plain text.

### CLI changes — `cmd/slk/main.go`

- Remove the package-level `version`, `commit`, `date` vars at lines 52-57.
  Import `github.com/gammons/slk/internal/version` instead.
- Update the `--version`/`-v`/`version` branch at lines 353-357 to print:

  ```
  <version.CLILongLine()>
  <version.AttributionLine()>
  Unofficial Slack client. Not affiliated with Slack Technologies, LLC.
  Uses Slack's internal browser protocol; may violate Slack's TOS. Use at your own risk.
  ```

- Update `printHelp()` at line 456 to substitute `version.Short()` in place
  of the bare `version` variable. No new attribution content added here.

### TUI changes — `internal/ui/help/model.go`

Add an optional footer line above the existing controls footer.

- New field on `Model`:

  ```go
  footer string
  ```

- New method:

  ```go
  func (m *Model) SetFooter(s string) { m.footer = s }
  ```

- In `renderBox`, when `m.footer != ""`, render it in the same muted style
  as the controls footer, on its own line, immediately above the existing
  controls footer. No blank line between the two footers.
- Width: use `lipgloss.NewStyle().MaxWidth(innerWidth)` so the line
  soft-wraps when the modal is at its narrow cap (~36 cols inner). The
  URL stays intact across the wrap.

### TUI wiring — `internal/ui/app.go`

In the constructor (near the existing `statusbar.SetHelpHint` call at line
~368), add:

```go
app.help.SetFooter(version.ModalFooter())
```

This runs once at startup, after `help.New()` (line ~314). The help modal
remains decoupled from the `version` package; only `app.go` knows about
the binding.

### Build configuration — `.goreleaser.yaml`

Update the ldflags block (lines 30-34) to target the new package path:

```yaml
ldflags:
  - -s -w
  - -X github.com/gammons/slk/internal/version.Version={{.Version}}
  - -X github.com/gammons/slk/internal/version.Commit={{.Commit}}
  - -X github.com/gammons/slk/internal/version.Date={{.Date}}
```

The brew formula test at line 159 (`assert_match "slk", shell_output(...)`)
keeps passing — CLI output still starts with `slk`.

### Smoke test for ldflag injection

Add a `Makefile` target `verify-version` that builds with explicit ldflags
and asserts the injected value reaches the binary. This catches the
failure mode where a future package rename drifts from the goreleaser
yaml path:

```sh
go build -ldflags "-X github.com/gammons/slk/internal/version.Version=test1.2.3" -o /tmp/slk-vtest ./cmd/slk
/tmp/slk-vtest --version | grep -q "slk vtest1.2.3" || { echo "ldflag injection broken"; exit 1; }
rm -f /tmp/slk-vtest
```

Wire this into `.github/workflows/ci.yml` as a new step in the `test`
job, after the existing `Test` step (line 41), invoking
`make verify-version`. Runs on every push to `main` and every PR.

## Visual layout

### `slk --version` output (dev build):

```
slk dev (commit none, built unknown)
by Grant Ammons — https://grant.dev
Unofficial Slack client. Not affiliated with Slack Technologies, LLC.
Uses Slack's internal browser protocol; may violate Slack's TOS. Use at your own risk.
```

Line 2's URL is OSC 8 wrapped.

### `?` help modal:

```
Keybindings
Press / to search

  k/up           up
  j/down         down
  …              …

slk v1.2.3 — by Grant Ammons — https://grant.dev   ← NEW footer line
/ search   esc/q close
```

At narrow modal widths (~36 inner cols) the new footer soft-wraps to two
lines via `MaxWidth`.

## Testing

### `internal/version/version_test.go`

- `Short()` returns `"dev"` when `Version == "dev"`.
- `Short()` returns `"v1.2.3"` when `Version == "1.2.3"`.
- `Short()` returns `"v1.2.3"` unchanged when `Version == "v1.2.3"`.
- `Short()` returns `"v1.2.3-next"` when `Version == "1.2.3-next"`.
- `CLILongLine()` returns `"slk dev (commit none, built unknown)"` with
  default vars.
- `AttributionLine()` contains the literal substring `by Grant Ammons`.
- `AttributionLine()` contains the OSC 8 opener
  `"\x1b]8;;https://grant.dev\x1b\\"` and closer `"\x1b]8;;\x1b\\"`.
- `ModalFooter()` equals `"slk " + Short() + " — " + AttributionLine()`.
- `lipgloss.Width(AttributionLine())` equals the visible-character count
  (`utf8.RuneCountInString("by Grant Ammons — https://grant.dev")`).
  This validates that lipgloss correctly ignores OSC 8 escape sequences
  when computing visible width.

### `internal/ui/help/model_test.go`

- `TestSetFooterRenders`: after `m.SetFooter("hello-footer")`, the output
  of `m.ViewOverlay(80, 24, "")` contains the substring `"hello-footer"`.
- `TestNoFooterByDefault`: with no `SetFooter` call, `ViewOverlay` output
  does not contain a stray blank line above the controls footer (compare
  line count to the existing baseline).

### Integration

The `make verify-version` smoke test described above covers the ldflag
injection path end-to-end. No Go-level `cmd/slk` test is added for this.

## Edge cases and risks

- **Local `go build` defaults.** `Version == "dev"` etc. Behavior matches
  current state; `Short()` returns `"dev"` (no `v` prefix).
- **OSC 8 width miscounting.** Mitigated by the `lipgloss.Width` test
  above. If lipgloss miscounts, fallback is to expose a parallel
  `AttributionLinePlain()` (no OSC 8) and use that for modal rendering,
  keeping OSC 8 for the CLI path. Decision deferred to implementation
  if the test fails.
- **goreleaser ldflag path drift.** Mitigated by `make verify-version`.
- **Backward compatibility.** `SetFooter` is additive on `help.Model`;
  callers and existing tests are unaffected.
- **`printHelp()` body content.** Intentionally unchanged beyond
  substituting `version.Short()` for the bare `version` var; the
  attribution line is scoped to `--version` and the modal only.
