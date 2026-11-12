package web

import "testing"

func TestValidTerminalPIN(t *testing.T) {
	for _, pin := range []string{"4711", "90210", "13579246"} {
		if err := validTerminalPIN(pin); err != nil {
			t.Errorf("%q sollte gültig sein: %v", pin, err)
		}
	}
	for _, pin := range []string{"", "123", "123456789", "12a4", "0000", "1234", "8765", "0123", "456789"} {
		if err := validTerminalPIN(pin); err == nil {
			t.Errorf("%q sollte ungültig sein", pin)
		}
	}
}
