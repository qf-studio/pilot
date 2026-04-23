// Package text provides text-layer primitives used across adapters,
// executor, and autopilot. It is a leaf package with no dependencies on
// other internal packages so that any adapter can import it without
// creating a cycle.
package text

import (
	"strings"
	"unicode"
)

// SanitizeUntrusted strips invisible Unicode format characters from
// untrusted external text (issue titles/bodies, chat messages, CI logs)
// before it reaches Claude Code.
//
// Threat model: ASCII smuggling / invisible-Unicode prompt injection.
// An attacker embeds instructions in characters humans cannot see but
// an LLM reads literally: the Tag block U+E0000..U+E007F (ASCII
// mirrored invisibly), zero-width characters U+200B/C/D and U+FEFF,
// bidi overrides U+202A..U+202E and isolates U+2066..U+2069, and
// variation selectors U+FE00..U+FE0F + U+E0100..U+E01EF.
//
// What is stripped:
//   - Every rune in Unicode general category Cf (Format) — this is the
//     single broadest rule and covers all the classes above plus the
//     rest of the format-control block.
//   - Explicit ranges U+E0000..U+E007F and U+E0100..U+E01EF as a
//     defense in depth; the Cf check should already catch them, but
//     Go's Unicode tables have historically drifted.
//
// What is preserved:
//   - The Cc whitespace trio \t (U+0009), \n (U+000A), \r (U+000D).
//     Markdown ingestion depends on newlines and tabs.
//   - All visible Unicode (letters, punctuation, emoji base codepoints,
//     symbols).
//
// Tradeoff: stripping U+200D (zero-width joiner) breaks emoji-ZWJ
// sequences — family emoji, profession emoji, skin-tone modifiers
// degrade to their base glyphs. For ticket sources this is irrelevant.
// For chat adapters (Telegram/Slack/Discord) echoed confirmations may
// render split emoji. Accepted: the threat model (autonomous LLM
// executor) prioritizes smuggling resistance over emoji fidelity.
//
// Not done here: NFC/NFKC normalization. Homoglyph attacks are a
// separate class and NFKC has its own corruption risks (e.g. fullwidth
// code-sample mangling). File a follow-up if needed.
//
// Returns the cleaned string and the number of runes stripped. Callers
// should emit a slog.Warn when stripped > 0 — that is the
// attack-in-progress telemetry signal.
func SanitizeUntrusted(s string) (clean string, stripped int) {
	if s == "" {
		return "", 0
	}

	// Fast path: scan once to see if any stripping is needed. Most
	// inbound text will be clean, so avoid the allocation of a builder.
	needs := false
	for _, r := range s {
		if shouldStrip(r) {
			needs = true
			break
		}
	}
	if !needs {
		return s, 0
	}

	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if shouldStrip(r) {
			stripped++
			continue
		}
		b.WriteRune(r)
	}
	return b.String(), stripped
}

// SanitizeUntrustedString is a convenience wrapper for call sites that
// do not want to log strip-count telemetry. Prefer SanitizeUntrusted at
// adapter boundaries where per-source observability is valuable.
func SanitizeUntrustedString(s string) string {
	clean, _ := SanitizeUntrusted(s)
	return clean
}

// shouldStrip reports whether a rune is an invisible format character
// that must not reach the LLM.
func shouldStrip(r rune) bool {
	// Preserve the only Cc whitespace we care about. Everything else in
	// Cc (NUL, bell, backspace, etc.) is also not in Cf so it falls
	// through to the visible-preserve path — that is fine: if a raw NUL
	// shows up the subsequent JSON marshaller or Claude subprocess will
	// reject it, which is the desired behavior.
	if r == '\t' || r == '\n' || r == '\r' {
		return false
	}
	// Explicit Tag-block defense-in-depth.
	if r >= 0xE0000 && r <= 0xE007F {
		return true
	}
	// Variation selectors U+FE00..U+FE0F. These are Mn (Mark,
	// nonspacing) not Cf, so the category check below would miss them.
	// They alter glyph presentation invisibly — useful for smuggling
	// visually identical strings with different semantics.
	if r >= 0xFE00 && r <= 0xFE0F {
		return true
	}
	// Variation-selector supplement (also Mn, not Cf).
	if r >= 0xE0100 && r <= 0xE01EF {
		return true
	}
	// Catch-all: any rune in Unicode General Category "Cf" (Format).
	// Includes U+200B ZWSP, U+200C ZWNJ, U+200D ZWJ, U+FEFF BOM,
	// U+202A..U+202E bidi overrides, U+2066..U+2069 isolates,
	// U+FE00..U+FE0F variation selectors, the Tag block, and ~200 more.
	return unicode.Is(unicode.Cf, r)
}
