package web

import "testing"

func TestAppVersionLess(t *testing.T) {
	cases := []struct {
		a, b     string
		less, ok bool
	}{
		{"1.0.0", "1.1.0", true, true},
		{"1.10.0", "1.9.0", false, true},
		{"1.9.0", "1.10.0", true, true},
		{"1.1", "1.1.0", false, true},
		{"1.1.0.0", "1.1", false, true},
		{"1", "1.0.1", true, true},
		{"2", "1.99.99", false, true},
		{"1.1.0", "1.1.0", false, true},
		{"", "1.0.0", false, false},
		{"abc", "1.0.0", false, false},
		{"1.x.0", "1.0.0", false, false},
		{"1..0", "1.0.0", false, false},
		{"1.0.0-beta", "1.0.0", false, false},
		{"99999999999999999999.0", "1.0", false, false},
	}
	for _, c := range cases {
		less, ok := appVersionLess(c.a, c.b)
		if less != c.less || ok != c.ok {
			t.Errorf("appVersionLess(%q,%q) = %v,%v, erwartet %v,%v", c.a, c.b, less, ok, c.less, c.ok)
		}
	}
}
