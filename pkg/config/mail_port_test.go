package config

import "testing"

// Der SMTP-Port hat keinen festen Standard mehr: leer bleibt 0, damit der
// Versand den üblichen Port zur Verschlüsselung wählt. Ein gesetzter Port
// bleibt unverändert.
func TestMailPortHasNoFixedDefault(t *testing.T) {
	t.Setenv("PDH_SMTP_PORT", "")
	cfg, err := Load()
	if err != nil {
		t.Skipf("Load: %v", err)
	}
	if cfg.Mail.Port != 0 {
		t.Errorf("Port ohne Eintrag: %d statt 0", cfg.Mail.Port)
	}
	t.Setenv("PDH_SMTP_PORT", "2465")
	cfg, err = Load()
	if err != nil {
		t.Skipf("Load: %v", err)
	}
	if cfg.Mail.Port != 2465 {
		t.Errorf("gesetzter Port: %d statt 2465", cfg.Mail.Port)
	}
	t.Setenv("PDH_SMTP_PORT", "587")
	if cfg, err = Load(); err == nil && cfg.Mail.Port != 587 {
		t.Errorf("bestehender Port 587: %d", cfg.Mail.Port)
	}
}
