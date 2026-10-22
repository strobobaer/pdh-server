package web

import (
	"testing"
	"time"
)

func TestPhotoToken(t *testing.T) {
	h := &Handler{jwtSecret: "geheim"}
	now := time.Now()
	id := "11111111-1111-7111-8111-111111111111"
	tok := h.photoToken("fault", id, now)
	if !h.checkPhotoToken("fault", id, tok, now) {
		t.Fatal("gültiger Schlüssel abgelehnt")
	}
	if h.checkPhotoToken("ticket", id, tok, now) || h.checkPhotoToken("fault", "22222222-2222-7222-8222-222222222222", tok, now) {
		t.Error("Schlüssel gilt nur für Art und Vorgang, für die er ausgestellt wurde")
	}
	if h.checkPhotoToken("fault", id, tok, now.Add(photoTokenTTL+time.Minute)) {
		t.Error("abgelaufener Schlüssel angenommen")
	}
	if h.checkPhotoToken("fault", id, "123.abc", now) || h.checkPhotoToken("fault", id, "", now) {
		t.Error("gefälschter Schlüssel angenommen")
	}
	if (&Handler{}).checkPhotoToken("fault", id, (&Handler{}).photoToken("fault", id, now), now) {
		t.Error("ohne Server-Geheimnis darf kein Schlüssel gelten")
	}
}
