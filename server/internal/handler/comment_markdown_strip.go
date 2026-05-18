package handler

import "strings"

// PUL-181: markdown-aware pre-processor for the skill auto-detect
// pipeline. The skillCandidateRe regex in comment_skill_autodetect.go
// doesn't understand markdown, so a comment that QUOTES "> /office-hours"
// or PASTES a fenced ``` /office-hours ``` block was getting flagged as
// the user invoking that skill. This stripper wipes blockquote lines,
// fenced code blocks, and inline code spans (replacing them with ASCII
// whitespace of equal length) before the regex runs. Out of scope for
// v1: lazy blockquote continuation, indented (4-space) code blocks, and
// unbalanced backtick spans — see Failure modes in the plan.

// stripMarkdownNonText returns content with markdown blockquote lines,
// fenced code blocks, and inline code spans replaced by ASCII spaces
// of equal length. Length preservation is a deliberate property — it
// keeps downstream offset-bearing tooling (Sentry breadcrumbs, future
// column-aware diagnostics) honest without translation tables.
//
// Algorithm (single line-by-line pass):
//
//   - If currently inside a fenced code block: replace the line with
//     spaces; if the line starts with the same fence marker that
//     opened the fence, exit fence state.
//   - Else if the line opens a fenced code block (starts with ``` or
//     ~~~ after at most 3 spaces/tabs of leading whitespace): replace
//     the line with spaces and enter fence state.
//   - Else if the line is a blockquote (starts with > after at most
//     3 spaces/tabs of leading whitespace): replace the line with
//     spaces.
//   - Else: strip inline code spans on the line.
func stripMarkdownNonText(content string) string {
	if content == "" {
		return content
	}

	lines := strings.Split(content, "\n")
	var inFence bool
	var fenceMarker string

	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")

		if inFence {
			if strings.HasPrefix(trimmed, fenceMarker) {
				inFence = false
				fenceMarker = ""
			}
			lines[i] = blankLine(line)
			continue
		}

		switch {
		case strings.HasPrefix(trimmed, "```"):
			inFence = true
			fenceMarker = "```"
			lines[i] = blankLine(line)
			continue
		case strings.HasPrefix(trimmed, "~~~"):
			inFence = true
			fenceMarker = "~~~"
			lines[i] = blankLine(line)
			continue
		}

		if strings.HasPrefix(trimmed, ">") {
			lines[i] = blankLine(line)
			continue
		}

		lines[i] = stripInlineCodeSpans(line)
	}

	return strings.Join(lines, "\n")
}

// blankLine returns a string of len(line) spaces. Newlines are not
// included — strings.Split removes them, and strings.Join restores
// them between elements.
func blankLine(line string) string {
	return strings.Repeat(" ", len(line))
}

// stripInlineCodeSpans replaces backtick-delimited code spans with
// spaces of equal length. CommonMark requires opening and closing
// backtick runs to be the same length; Go RE2 has no backreferences,
// so we walk runs of backticks manually instead of relying on regex.
//
// Pairing: scan runs left→right. Each unpaired opener takes the next
// available run of equal length as its closer. Unpaired openers
// (unbalanced backticks — see plan's known limitations) are left as
// is, matching CommonMark, which also treats them as literal text.
func stripInlineCodeSpans(line string) string {
	runs := backtickRuns(line)
	if len(runs) < 2 {
		return line
	}

	b := []byte(line)
	paired := make([]bool, len(runs))
	for i := 0; i < len(runs); i++ {
		if paired[i] {
			continue
		}
		open := runs[i]
		for j := i + 1; j < len(runs); j++ {
			if paired[j] || runs[j].length != open.length {
				continue
			}
			closeRun := runs[j]
			for k := open.start; k < closeRun.start+closeRun.length; k++ {
				b[k] = ' '
			}
			paired[i] = true
			paired[j] = true
			break
		}
	}
	return string(b)
}

// backtickRun describes a single run of consecutive backticks.
type backtickRun struct {
	start  int
	length int
}

// backtickRuns returns every maximal run of consecutive '`' in s.
func backtickRuns(s string) []backtickRun {
	var runs []backtickRun
	for i := 0; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		start := i
		for i < len(s) && s[i] == '`' {
			i++
		}
		runs = append(runs, backtickRun{start: start, length: i - start})
	}
	return runs
}
