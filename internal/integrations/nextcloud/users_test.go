package nextcloud

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode"
)

func TestCreateUser(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); u != "admin" || p != "pw" || r.URL.Path != "/ocs/v1.php/cloud/users" || r.Header.Get("OCS-APIRequest") != "true" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = r.ParseForm()
		got = map[string]string{"userid": r.FormValue("userid"), "password": r.FormValue("password"), "email": r.FormValue("email"), "displayName": r.FormValue("displayName")}
		code := "100"
		if got["userid"] == "vorhanden" {
			code = "102"
		}
		_, _ = w.Write([]byte(`{"ocs":{"meta":{"status":"ok","statuscode":` + code + `,"message":""},"data":{}}}`))
	}))
	defer srv.Close()
	t.Setenv("PDH_NEXTCLOUD_BASEURL", srv.URL+"/")
	t.Setenv("PDH_NEXTCLOUD_USERNAME", "admin")
	t.Setenv("PDH_NEXTCLOUD_PASSWORD", "pw")
	c := UserClientFromEnv()
	pw, err := c.CreateUser(context.Background(), NewUser{UserID: "max.muster", DisplayName: "Max Muster", Email: "max@firma.de"})
	if err != nil || pw == "" || got["password"] != pw || got["userid"] != "max.muster" || got["email"] != "max@firma.de" || got["displayName"] != "Max Muster" {
		t.Fatalf("pw=%q err=%v got=%v", pw, err, got)
	}
	if _, err := c.CreateUser(context.Background(), NewUser{UserID: "vorhanden"}); !errors.Is(err, ErrUserExists) {
		t.Errorf("vorhanden: %v", err)
	}
	t.Setenv("PDH_NEXTCLOUD_PASSWORD", "falsch")
	if _, err := UserClientFromEnv().CreateUser(context.Background(), NewUser{UserID: "x"}); err == nil || !strings.Contains(err.Error(), "Admin-Rechte") {
		t.Errorf("ohne Rechte: %v", err)
	}
	t.Setenv("PDH_NEXTCLOUD_BASEURL", "")
	if UserClientFromEnv().Enabled() {
		t.Error("ohne Adresse eingerichtet")
	}
}

func TestSanitizeUserIDAndPassword(t *testing.T) {
	for in, want := range map[string]string{"m.strobl": "m.strobl", "Jürgen Weiß": "Juergen.Weiss", " -a/b- ": "ab", "x@firma.de": "x@firma.de"} {
		if got := SanitizeUserID(in); got != want {
			t.Errorf("%q: %q, erwartet %q", in, got, want)
		}
	}
	for i := 0; i < 50; i++ {
		pw := RandomPassword(20)
		var up, lo, dig, sp bool
		for _, r := range pw {
			up = up || unicode.IsUpper(r)
			lo = lo || unicode.IsLower(r)
			dig = dig || unicode.IsDigit(r)
			sp = sp || strings.ContainsRune("!#%+-=?@", r)
		}
		if len(pw) != 20 || !up || !lo || !dig || !sp {
			t.Fatalf("schwaches Passwort %q", pw)
		}
	}
}

func TestCheckAdmin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pw, _ := r.BasicAuth()
		switch {
		case pw != "richtig":
			w.WriteHeader(http.StatusUnauthorized)
		case user == "pdhNCadmin":
			_, _ = w.Write([]byte(`{"ocs":{"meta":{"statuscode":100},"data":{"groups":["users","admin"]}}}`))
		default:
			_, _ = w.Write([]byte(`{"ocs":{"meta":{"statuscode":100},"data":{"groups":["users"]}}}`))
		}
	}))
	defer srv.Close()
	if ok, err := NewUserClient(srv.URL, "pdhNCadmin", "richtig").CheckAdmin(context.Background()); err != nil || !ok {
		t.Errorf("Admin: %v %v", ok, err)
	}
	if ok, err := NewUserClient(srv.URL, "eva", "richtig").CheckAdmin(context.Background()); err != nil || ok {
		t.Errorf("kein Admin: %v %v", ok, err)
	}
	if _, err := NewUserClient(srv.URL, "pdhNCadmin", "falsch").CheckAdmin(context.Background()); !errors.Is(err, ErrLoginFailed) {
		t.Errorf("falsches Passwort: %v", err)
	}
}
