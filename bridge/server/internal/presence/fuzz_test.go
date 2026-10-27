package presence

import (
	"testing"
	"unicode"
	"unicode/utf8"
)

func FuzzCleanText(f *testing.F) {
	for _, seed := range []string{"Undone", "", " ", "A", "evil\u202etxt", "zero\u200bwidth", "\xff\xfe", "ペルソナ3"} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input string) {
		out := cleanText(input, "")
		if out == "" {
			return
		}
		if !utf8.ValidString(out) {
			t.Fatalf("invalid UTF-8 output %q", out)
		}
		n := utf8.RuneCountInString(out)
		if n < minTextRunes || n > maxTextRunes {
			t.Fatalf("output has %d runes: %q", n, out)
		}
		for _, r := range out {
			if r != padRune && (unicode.IsControl(r) || unicode.Is(unicode.Cf, r)) {
				t.Fatalf("output contains %U: %q", r, out)
			}
		}
	})
}
