package kernel

import "testing"

func TestChangedFilesTextNamesTheFirstFour(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{"lean/Baloon/GenerateDriver.lean"}, " (lean/Baloon/GenerateDriver.lean)"},
		{[]string{"a", "b", "c", "d"}, " (a b c d)"},
		{[]string{"a", "b", "c", "d", "e", "f"}, " (a b c d (+2 more))"},
	} {
		if got := changedFilesText(c.in); got != c.want {
			t.Fatalf("%v: %q, want %q", c.in, got, c.want)
		}
	}
}
