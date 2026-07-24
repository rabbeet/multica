package telegramoutbound

import (
	"strings"
	"testing"
)

func TestSplitByLines_ShortText(t *testing.T) {
	chunks := SplitByLines("hello world")
	if len(chunks) != 1 || chunks[0] != "hello world" {
		t.Fatalf("got %v", chunks)
	}
}

func TestSplitByLines_EmptyInputStillReturnsOne(t *testing.T) {
	chunks := SplitByLines("")
	if len(chunks) != 1 || chunks[0] != "" {
		t.Fatalf("empty input must not silently drop: got %v", chunks)
	}
}

func TestSplitByLines_ManyShortLinesConcatenate(t *testing.T) {
	// Ten "line N\n" — each ~7 bytes; single chunk fits.
	var b strings.Builder
	for i := 0; i < 10; i++ {
		b.WriteString("line X\n")
	}
	chunks := SplitByLines(b.String())
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0] != b.String() {
		t.Errorf("content not preserved")
	}
}

func TestSplitByLines_BudgetSplitOnLFBoundary(t *testing.T) {
	// 5 lines of 1000 bytes each — total 5005; each line under
	// budget, but 2 lines already crowd 2000 → expect 2 chunks.
	line := strings.Repeat("x", 999) + "\n"
	input := strings.Repeat(line, 5)
	chunks := SplitByLines(input)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	// Concat preserves original bit-for-bit.
	if strings.Join(chunks, "") != input {
		t.Errorf("concat mismatch")
	}
	// Every chunk within budget.
	for i, c := range chunks {
		if len(c) > maxMessageBytes {
			t.Errorf("chunk %d over budget: len=%d", i, len(c))
		}
	}
}

func TestSplitByLines_SingleLongLineSplitOnRuneBoundary(t *testing.T) {
	// One line ~10000 bytes, no LFs — must split on rune boundary,
	// never mid-codepoint. Use kg-boundary UTF-8 (Ж = 2 bytes each).
	long := strings.Repeat("Ж", maxMessageBytes) // 2*4000 = 8000 bytes
	chunks := SplitByLines(long)
	if len(chunks) < 2 {
		t.Fatalf("expected splits, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > maxMessageBytes {
			t.Errorf("chunk %d over budget: len=%d", i, len(c))
		}
		// Each chunk must decode as valid UTF-8 (rune boundary
		// preserved). Non-rune-safe split would yield replacement runes.
		for _, r := range c {
			if r == '�' {
				t.Errorf("chunk %d has replacement rune (split mid-codepoint)", i)
			}
		}
	}
	if strings.Join(chunks, "") != long {
		t.Errorf("concat mismatch")
	}
}

func TestWithProgressPrefix_SingleChunkUnchanged(t *testing.T) {
	in := []string{"only"}
	out := WithProgressPrefix(in)
	if len(out) != 1 || out[0] != "only" {
		t.Errorf("single chunk should be untouched: %v", out)
	}
}

func TestWithProgressPrefix_MultiChunkPrefixed(t *testing.T) {
	in := []string{"a", "b", "c"}
	out := WithProgressPrefix(in)
	want := []string{"(1/3) a", "(2/3) b", "(3/3) c"}
	if len(out) != len(want) {
		t.Fatalf("len got %d want %d", len(out), len(want))
	}
	for i := range want {
		if out[i] != want[i] {
			t.Errorf("[%d] got %q want %q", i, out[i], want[i])
		}
	}
}
