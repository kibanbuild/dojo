package catalogload

import "testing"

func TestVersion(t *testing.T) {
	for in, want := range map[string]string{
		"0123456789abcdef0123456789abcdef01234567": "0123456789ab",
		"  0123456789AB ":                          "0123456789ab",
	} {
		if got, err := Version(in); err != nil || got != want {
			t.Errorf("Version(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "abc", "0123456789a", "0123456789ag", "main"} {
		if _, err := Version(in); err == nil {
			t.Errorf("Version(%q) accepted", in)
		}
	}
}
