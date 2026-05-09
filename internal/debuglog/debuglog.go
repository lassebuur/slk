// Package debuglog provides categorized debug logging for slk.
//
// When SLK_DEBUG is set in the environment, Init opens slk-debug.log
// in the current working directory (truncating any existing file) and
// configures a package-internal logger. When unset, every category
// function is a fast no-op via an atomic.Bool flag — Sprintf-style
// args still get evaluated by Go's calling convention, but no
// formatting work occurs inside the package.
//
// Categories:
//   - Cache     — messages cache + reconciliation
//   - ImgFetch  — image fetcher lifecycle
//   - ImgRender — image render sizing + protocol decisions
//   - WS        — websocket events
//   - General   — misc / catch-all
//
// All output goes to a single file. Categories are encoded as inline
// tag prefixes (e.g. "[cache] ...") so users can grep to slice the
// log.
package debuglog

import (
	"sync/atomic"
)

var enabled atomic.Bool

// Enabled reports whether logging is active. Cheap (atomic.Bool load).
func Enabled() bool {
	return enabled.Load()
}
