package term

import "testing"

func TestStripANSI(t *testing.T) {
	cases := map[string]string{
		"\x1b[32mgreen\x1b[0m":     "green",
		"\x1b[1;31mbold red\x1b[m": "bold red",
		"\x1b[?25lhidden cursor":   "hidden cursor",
		"plain [brackets]":         "plain [brackets]",
	}
	for in, want := range cases {
		if got := StripANSI(in); got != want {
			t.Errorf("StripANSI(%q) = %q, want %q", in, got, want)
		}
	}
}
