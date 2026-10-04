package clip

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestATextThatFitsIsReturnedAsItIs(t *testing.T) {
	for _, text := range []string{"", "short", strings.Repeat("a", 10)} {
		if got := Cut(text, 10); got != text {
			t.Errorf("Cut(%q, 10) = %q", text, got)
		}
	}
}

func TestALongTextIsCutAndSaysSo(t *testing.T) {
	if got := Cut(strings.Repeat("a", 50), 10); got != strings.Repeat("a", 10)+"..." {
		t.Errorf("got %q", got)
	}
	if got := Cut("abcdef", 0); got != "..." {
		t.Errorf("a limit of zero: %q", got)
	}
	if got := Cut("abcdef", -5); got != "..." {
		t.Errorf("a negative limit: %q", got)
	}
}

// The cut never leaves half of a character, wherever it falls: a character of two, three
// and four bytes, at each of the places where the limit may land.
func TestACutNeverLeavesHalfOfACharacter(t *testing.T) {
	for _, unit := range []string{"é", "€", "😀"} {
		text := strings.Repeat(unit, 20)
		for limit := 0; limit < len(text); limit++ {
			got := Cut(text, limit)
			if !utf8.ValidString(got) {
				t.Fatalf("%q at %d bytes: %q is not valid UTF-8", unit, limit, got)
			}
			if !strings.HasSuffix(got, "...") || len(got)-3 > limit {
				t.Fatalf("%q at %d bytes: %q is too long or has no ellipsis", unit, limit, got)
			}
			// ...and it keeps as much as fits: at most one character less than the limit.
			if kept := len(got) - 3; limit-kept >= utf8.UTFMax {
				t.Fatalf("%q at %d bytes: only %d bytes kept", unit, limit, kept)
			}
		}
	}
}

// The lead byte of a character was the case that a cut which only removes continuation bytes
// left behind.
func TestACutAfterTheFirstByteOfACharacterDropsThatByteToo(t *testing.T) {
	if got := Cut("aé", 2); got != "a..." {
		t.Errorf("got %q (% x)", got, got)
	}
}

func TestATextThatWasNotValidComesOutValidWhenItIsCollapsed(t *testing.T) {
	got := Collapse("ok \xff\xfe bad", 100)
	if !utf8.ValidString(got) || !strings.HasPrefix(got, "ok ") {
		t.Errorf("got %q", got)
	}
}

func TestCollapseMakesOneLineOutOfAnyText(t *testing.T) {
	got := Collapse("  first\n\n  second\tthird \r\n fourth  ", 100)
	if got != "first second third fourth" {
		t.Errorf("got %q", got)
	}
}

// What a terminal would obey is not repeated.
func TestControlCharactersAreNotRepeated(t *testing.T) {
	text := "before\x1b[2J\x1b]0;title\x07after\x00end\x7fdel"
	for name, got := range map[string]string{"collapse": Collapse(text, 200), "lines": Lines(text, 200)} {
		for _, r := range got {
			if r < ' ' && r != '\n' && r != '\t' || r == 0x7f {
				t.Errorf("%s: %q still has the control character %q", name, got, r)
			}
		}
		if !strings.Contains(got, "before") || !strings.Contains(got, "after") {
			t.Errorf("%s: the text was lost: %q", name, got)
		}
	}
}

func TestCollapseCutsAfterItHasCleanedTheText(t *testing.T) {
	// 300 bytes of spaces and escapes would be nothing once cleaned, and must not count.
	got := Collapse(strings.Repeat("\x1b ", 150)+"end", 10)
	if got != "end" {
		t.Errorf("got %q", got)
	}
	if got := Collapse(strings.Repeat("word ", 100), 12); got != "word word wo..." {
		t.Errorf("got %q", got)
	}
}

func TestLinesKeepsTheLinesAndTheTabs(t *testing.T) {
	got := Lines("first line\n\tsecond line\r\nthird", 200)
	if got != "first line\n\tsecond line \nthird" {
		t.Errorf("got %q", got)
	}
	if got := Lines("a\nb\nc\nd", 4); got != "a\nb\n..." {
		t.Errorf("got %q", got)
	}
}
