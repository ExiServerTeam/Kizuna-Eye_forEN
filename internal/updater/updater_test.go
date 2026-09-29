package updater

import "testing"

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in   string
		want []int
	}{
		{"v0.6.1", []int{0, 6, 1}},
		{"0.6.1", []int{0, 6, 1}},
		{"v1.0.0", []int{1, 0, 0}},
		{"v1.2.3-rc1", []int{1, 2, 3}},
		{"v1.2.3+build", []int{1, 2, 3}},
	}
	for _, c := range cases {
		got := parseVersion(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("parseVersion(%q) = %v, want %v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("parseVersion(%q) = %v, want %v", c.in, got, c.want)
			}
		}
	}
}

func TestIsNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"v0.6.2", "v0.6.1", true},
		{"v0.6.1", "v0.6.1", false},
		{"v0.6.0", "v0.6.1", false},
		{"v0.7.0", "v0.6.9", true},
		{"v1.0.0", "v0.9.9", true},
		{"v0.6.10", "v0.6.9", true},
		{"v0.6.1", "0.6.1", false},
	}
	for _, c := range cases {
		if got := isNewer(c.latest, c.current); got != c.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}
