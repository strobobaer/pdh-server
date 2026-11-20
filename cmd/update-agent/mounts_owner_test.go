package main

// Besitzer eingebundener SMB-Laufwerke: der Benutzer, unter dem der Dienst „pdh“
// wirklich läuft (User=/Group= der Einheit). Vorher galt fest der Benutzer „pdh“ –
// lief PDH als anderer Benutzer, durfte es auf dem Laufwerk nicht schreiben.

import (
	"context"
	"errors"
	"os/user"
	"testing"
)

func TestParseUnitOwner(t *testing.T) {
	for _, c := range []struct{ in, u, g string }{
		{"User=michael\nGroup=\n", "michael", ""},
		{"Group=pdh\r\nUser=pdh\r\n", "pdh", "pdh"},
		{"User=\nGroup=\n", "", ""},
		{"", "", ""},
		{"Unsinn\nUser= svc \n", "svc", ""},
	} {
		if u, g := parseUnitOwner(c.in); u != c.u || g != c.g {
			t.Errorf("parseUnitOwner(%q) = %q, %q – erwartet %q, %q", c.in, u, g, c.u, c.g)
		}
	}
}

func TestOwnerIDs(t *testing.T) {
	lu := func(name string) (*user.User, error) {
		switch name {
		case "michael":
			return &user.User{Username: "michael", Uid: "1000", Gid: "1000"}, nil
		case "root":
			return &user.User{Username: "root", Uid: "0", Gid: "0"}, nil
		case "toor":
			return &user.User{Username: "toor", Uid: "0", Gid: "0"}, nil
		}
		return nil, user.UnknownUserError(name)
	}
	lg := func(name string) (*user.Group, error) {
		if name == "pdh" {
			return &user.Group{Name: "pdh", Gid: "983"}, nil
		}
		return nil, user.UnknownGroupError(name)
	}
	for _, c := range []struct {
		u, g     string
		uid, gid string
		ok       bool
	}{
		{"michael", "", "1000", "1000", true},
		{"michael", "pdh", "1000", "983", true},
		{"michael", "4343", "1000", "4343", true}, // Gruppe als Zahl ohne Eintrag
		{"michael", "gibtsnicht", "1000", "1000", true},
		{"4242", "", "4242", "4242", true}, // Benutzer als Zahl ohne Eintrag
		{"", "", "", "", false},            // Einheit ohne User= (läuft als root) oder unbekannt
		{"root", "", "", "", false},
		{"toor", "", "", "", false}, // Kennung 0 unter anderem Namen
		{"0", "", "", "", false},
		{"gibtsnicht", "", "", "", false},
	} {
		uid, gid, ok := ownerIDs(c.u, c.g, lu, lg)
		if uid != c.uid || gid != c.gid || ok != c.ok {
			t.Errorf("ownerIDs(%q, %q) = %q, %q, %v – erwartet %q, %q, %v", c.u, c.g, uid, gid, ok, c.uid, c.gid, c.ok)
		}
	}
}

func TestMountOwnerFromUnit(t *testing.T) {
	t.Setenv("PDH_MOUNT_UID", "")
	t.Setenv("PDH_MOUNT_GID", "")
	asked := 0
	a := &updateAgent{mode: "systemd", mountUnitShow: func(context.Context) (string, error) {
		asked++
		return "User=4242\nGroup=4343\n", nil
	}}
	if uid, gid := a.mountOwner(context.Background()); uid != "4242" || gid != "4343" || asked != 1 {
		t.Fatalf("systemd: %q, %q (gefragt %d×) – erwartet 4242, 4343 aus der Einheit", uid, gid, asked)
	}

	// Nur die Gruppe fest vorgegeben: Benutzer aus der Einheit, Gruppe bleibt die Vorgabe.
	t.Setenv("PDH_MOUNT_GID", "777")
	if uid, gid := a.mountOwner(context.Background()); uid != "4242" || gid != "777" {
		t.Fatalf("PDH_MOUNT_GID: %q, %q – erwartet 4242, 777", uid, gid)
	}
	// … auch beim Rückfall (Einheit nicht lesbar).
	r := &updateAgent{mode: "systemd", mountUnitShow: func(context.Context) (string, error) { return "", errors.New("weg") }}
	if _, gid := r.mountOwner(context.Background()); gid != "777" {
		t.Fatalf("PDH_MOUNT_GID im Rückfall: %q – erwartet 777", gid)
	}
	t.Setenv("PDH_MOUNT_GID", "")

	// Feste Vorgabe hat Vorrang, die Einheit wird dann nicht gefragt.
	t.Setenv("PDH_MOUNT_UID", "1500")
	asked = 0
	if uid, gid := a.mountOwner(context.Background()); uid != "1500" || gid != "1500" || asked != 0 {
		t.Fatalf("PDH_MOUNT_UID: %q, %q (gefragt %d×)", uid, gid, asked)
	}
	t.Setenv("PDH_MOUNT_UID", "")

	// Docker: Einheit gibt es nicht, Vorgabe 10001 wie bisher.
	d := &updateAgent{mode: "docker", mountUnitShow: a.mountUnitShow}
	asked = 0
	if uid, gid := d.mountOwner(context.Background()); uid != "10001" || gid != "10001" || asked != 0 {
		t.Fatalf("docker: %q, %q (gefragt %d×)", uid, gid, asked)
	}

	// Einheit nicht lesbar oder läuft als root: bisherige Regel (Benutzer „pdh“, sonst 10001), nie 0.
	for _, show := range []func(context.Context) (string, error){
		func(context.Context) (string, error) { return "", errors.New("systemctl fehlt") },
		func(context.Context) (string, error) { return "User=root\nGroup=root\n", nil },
	} {
		f := &updateAgent{mode: "systemd", mountUnitShow: show}
		uid, gid := f.mountOwner(context.Background())
		if uid == "" || uid == "0" || gid == "" || gid == "0" {
			t.Fatalf("Rückfall: %q, %q – erwartet Benutzer pdh oder 10001", uid, gid)
		}
		if u, err := user.Lookup("pdh"); err == nil {
			if uid != u.Uid {
				t.Fatalf("Rückfall: %q – erwartet Kennung von pdh %q", uid, u.Uid)
			}
		} else if uid != "10001" {
			t.Fatalf("Rückfall: %q – erwartet 10001", uid)
		}
	}
}
