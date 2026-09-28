package alert

import "testing"

func TestFormatStorageUnits(t *testing.T) {
	const MB = uint64(1024 * 1024)
	const GB = MB * 1024
	const TB = GB * 1024

	cases := []struct {
		name string
		in   uint64
		want string
	}{
		{"terabytes", 2 * TB, "2.00 TB"},
		{"gigabytes", 5 * GB, "5.0 GB"},
		// A nearly full disk must not round down to "0.0 GB".
		{"small megabytes", 400 * MB, "400 MB"},
		{"zero", 0, "0 MB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatStorage(tc.in); got != tc.want {
				t.Errorf("formatStorage(%d) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
