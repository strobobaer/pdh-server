package web

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

func TestAvatarProcess(t *testing.T) {
	// Querformat mit transparentem Rand: wird mittig quadratisch, 256 px, JPEG
	src := image.NewNRGBA(image.Rect(0, 0, 900, 600))
	for y := 0; y < 600; y++ {
		for x := 0; x < 900; x++ {
			c := color.NRGBA{200, 40, 40, 255}
			if x < 150 || x >= 750 {
				c = color.NRGBA{0, 0, 255, 0} // außerhalb des Zuschnitts, transparent
			}
			src.Set(x, y, c)
		}
	}
	var in bytes.Buffer
	if err := png.Encode(&in, src); err != nil {
		t.Fatal(err)
	}
	out, err := avatarProcess(&in)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("kein JPEG: %v", err)
	}
	if b := img.Bounds(); b.Dx() != avatarSize || b.Dy() != avatarSize {
		t.Fatalf("Größe %v", b)
	}
	// Mitte kommt aus dem roten Bereich (Zuschnitt 150…750)
	r, g, _, _ := img.At(128, 128).RGBA()
	if r>>8 < 150 || g>>8 > 90 {
		t.Errorf("Zuschnitt falsch: r=%d g=%d", r>>8, g>>8)
	}
	if _, err := avatarProcess(strings.NewReader("kein bild")); err == nil {
		t.Error("Unsinn muss abgelehnt werden")
	}
	tiny := image.NewRGBA(image.Rect(0, 0, 8, 8))
	var tb bytes.Buffer
	_ = png.Encode(&tb, tiny)
	if _, err := avatarProcess(&tb); err == nil {
		t.Error("zu kleines Bild muss abgelehnt werden")
	}
}

func TestAvatarURLAndNotice(t *testing.T) {
	if avatarURL("u1", "") != "" {
		t.Error("ohne Bild keine Adresse")
	}
	a, b := avatarURL("u1", "u1-1.jpg"), avatarURL("u1", "u1-2.jpg")
	if !strings.HasPrefix(a, "/avatar/u1?v=") || a == b {
		t.Errorf("Adresse muss sich mit dem Bild ändern: %s / %s", a, b)
	}
	if avatarURL(pdhSystemUserID, "") != "/avatar/service" {
		t.Error("Service zeigt das Logo")
	}
	if got := withNotice("/account", "err", "zu groß"); got != "/account?notice=Fehler%3A+zu+gro%C3%9F" {
		t.Errorf("Konto-Rückmeldung: %s", got)
	}
	if got := withNotice("/users/u1?tab=master", "notice", "ok"); got != "/users/u1?tab=master&msg=ok" {
		t.Errorf("Benutzerstamm-Rückmeldung: %s", got)
	}
}

func TestAvatarPagesRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	acc := AccountPageData{UserID: "u1", Initials: "MS", AvatarBg: "#6366f1"}
	acc.UserAvatar = "/avatar/u1?v=abcd"
	out := renderPage(t, tmpl, "account", acc)
	for _, want := range []string{`action="/users/u1/avatar"`, `enctype="multipart/form-data"`, `src="/avatar/u1?v=abcd"`, `name="remove" value="1"`, `class="tu-av"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Mein Konto ohne %q", want)
		}
	}
	list := renderPage(t, tmpl, "users", UsersPageData{Users: []UserView{{ID: "u2", FullName: "Eva", Initials: "E", AvatarURL: "/avatar/u2?v=1"}, {ID: "u3", FullName: "Max", Initials: "M"}}})
	if !strings.Contains(list, `src="/avatar/u2?v=1"`) || !strings.Contains(list, ">M</div>") {
		t.Error("Benutzerliste: Bild bzw. Initialen")
	}
}
