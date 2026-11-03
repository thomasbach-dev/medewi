package leva

import "testing"

func TestFirmwareVersion(t *testing.T) {
	for _, tc := range []struct{ id, want string }{
		{"$id: leva! (3.2)", "3.2"},
		{"$id: leva!", ""},
		{"", ""},
		{"$id: leva! (3.2.1-beta)", "3.2.1-beta"},
	} {
		if got := FirmwareVersion(tc.id); got != tc.want {
			t.Errorf("FirmwareVersion(%q) = %q, want %q", tc.id, got, tc.want)
		}
	}
}
