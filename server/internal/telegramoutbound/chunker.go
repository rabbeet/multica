package telegramoutbound

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// maxMessageBytes is the per-chunk budget we send to Bot API. Bot API
// enforces 4096 UTF-8 code points on sendMessage; we reserve ~96 code
// points for the "(k/N) " prefix and a trailing newline so callers can
// send the raw chunk without post-processing.
const maxMessageBytes = 4000

// SplitByLines breaks text into chunks each ≤maxMessageBytes UTF-8
// bytes, preferring LF boundaries. When a single line exceeds the
// budget, the line is split on rune boundaries (never mid-codepoint).
//
// Guarantees:
//   - concat(result) equals input rune-for-rune (LF separators preserved).
//   - each chunk is ≤maxMessageBytes bytes.
//   - if len(input)==0, returns [""] so callers still send one message
//     (no "silent empty comment" bug).
//   - if len(result)>1, callers should prepend "(k/N) " themselves via
//     WithProgressPrefix.
func SplitByLines(text string) []string {
	if text == "" {
		return []string{""}
	}
	if len(text) <= maxMessageBytes {
		return []string{text}
	}

	var chunks []string
	var current strings.Builder
	current.Grow(maxMessageBytes)

	flush := func() {
		if current.Len() > 0 {
			chunks = append(chunks, current.String())
			current.Reset()
		}
	}

	// SplitAfter keeps the trailing "\n" attached to each line, so
	// concat(lines)==text bit-for-bit. Splitting on lines guarantees
	// the common case (comments with paragraph structure) breaks at
	// paragraph boundaries.
	for _, line := range strings.SplitAfter(text, "\n") {
		if line == "" {
			// Only trailing empty string from SplitAfter; skip.
			continue
		}
		// If this single line is longer than the budget, break it
		// down further on rune boundaries.
		if len(line) > maxMessageBytes {
			flush()
			for _, piece := range splitLongRuneSafe(line, maxMessageBytes) {
				chunks = append(chunks, piece)
			}
			continue
		}
		// If adding this line would overflow, flush what we have.
		if current.Len()+len(line) > maxMessageBytes {
			flush()
		}
		current.WriteString(line)
	}
	flush()

	// Trailing "\n" got attached to the last line; if that pushed the
	// last chunk to exactly maxMessageBytes+1, the flush already
	// happened. In the ordinary case chunks is now the answer.
	return chunks
}

// splitLongRuneSafe breaks a single long string (no LFs) into pieces
// each ≤max bytes, never in the middle of a UTF-8 code point.
func splitLongRuneSafe(s string, max int) []string {
	var out []string
	for len(s) > 0 {
		if len(s) <= max {
			out = append(out, s)
			return out
		}
		// Walk backwards from max to find a valid rune boundary.
		cut := max
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		if cut == 0 {
			// s starts mid-rune (unreachable for valid UTF-8), take
			// the whole slice to avoid infinite loop.
			cut = max
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	return out
}

// WithProgressPrefix returns chunks with "(k/N) " prefix on every
// chunk when there are more than one. Called by the scheduler right
// before send so the raw SplitByLines output is easier to test in
// isolation.
func WithProgressPrefix(chunks []string) []string {
	if len(chunks) <= 1 {
		return chunks
	}
	out := make([]string, len(chunks))
	for i, chunk := range chunks {
		out[i] = fmt.Sprintf("(%d/%d) %s", i+1, len(chunks), chunk)
	}
	return out
}
