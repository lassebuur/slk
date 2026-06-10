package emoji

//go:generate go run gen_slacknames.go

import "fmt"

// CanonicalSlackName resolves a picker shortcode (no colons) to the emoji
// name Slack's reactions.add / reactions.remove API accepts. The customs
// map is the workspace's emoji.list (name -> URL or "alias:target"); pass
// nil if unavailable.
//
// Our picker is populated from kyokomi/emoji's CodeMap, which carries
// CLDR-style aliases (e.g. "thumbs_up", "OK_hand", "check_mark") that
// Slack does not recognize and rejects as invalid_name. Slack identifies
// a standard emoji by its Unicode codepoint and accepts exactly one
// canonical short_name per glyph (e.g. "+1" for 👍). This mirrors what
// Slack's own web client sends.
//
// Resolution order:
//  1. Custom-emoji shadow: a workspace custom emoji (or custom alias) wins
//     over any standard name — Slack stores it under that exact name, so
//     it MUST be sent verbatim. Checked first; without this a custom
//     ":facepalm:" would be rewritten to the standard ":face_palm:" and
//     the wrong (standard) reaction would be applied.
//  2. Skin-tone suffix ("+1_tone3" / "+1::skin-tone-3"): canonicalize the
//     base name and re-attach Slack's "::skin-tone-N" form.
//  3. Direct alias hit: the name is already a known Slack short_name or
//     alias — return its canonical form.
//  4. Fallback: return the name unchanged (unknown name sent verbatim).
//
// Since the picker is now sourced from the iamcal table (only Slack-valid
// names), step 3 covers every standard pick; the old kyokomi-codepoint
// fallback is no longer needed.
func CanonicalSlackName(name string, customs map[string]string) string {
	if _, ok := customs[name]; ok {
		return name
	}
	if base, tone, ok := splitSkinToneSuffix(name); ok {
		return fmt.Sprintf("%s::skin-tone-%d", CanonicalSlackName(base, customs), tone)
	}
	if canonical, ok := slackNameToCanonical[name]; ok {
		return canonical
	}
	return name
}
