package netsync

import "testing"

func TestNormMAC(t *testing.T) {
	tests := []struct{ in, want string }{
		{"AA-BB-CC-DD-EE-FF", "aa:bb:cc:dd:ee:ff"},
		{"aabb.ccdd.eeff", "aa:bb:cc:dd:ee:ff"},
		{"aabbccddeeff", "aa:bb:cc:dd:ee:ff"},
		{"AA:BB:CC:DD:EE:FF", "aa:bb:cc:dd:ee:ff"},
		{" aa:bb:cc:dd:ee:ff ", "aa:bb:cc:dd:ee:ff"},
		{"", ""},
		{"aa:bb:cc", ""},
		{"gg:bb:cc:dd:ee:ff", ""},
		{"aa:bb:cc:dd:ee:ff:00", ""},
	}
	for _, tc := range tests {
		if got := NormMAC(tc.in); got != tc.want {
			t.Errorf("NormMAC(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
