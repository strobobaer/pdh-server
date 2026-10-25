package main

import (
	"strings"
	"testing"
	"time"
)

func TestJoinCode(t *testing.T) {
	j := joinCode{Addrs: []string{"10.0.0.1:7480"}, FP: "abcd", Secret: "geheim", Role: "node", Commit: "c", Cluster: "id1"}
	s := j.String()
	if strings.ContainsAny(s, "=+/ ") {
		t.Fatalf("kein URL-sicherer Text: %q", s)
	}
	g, err := parseJoinCode(s)
	if err != nil || g.FP != "abcd" || g.Secret != "geheim" || g.Cluster != "id1" || g.Addrs[0] != "10.0.0.1:7480" {
		t.Fatalf("%+v %v", g, err)
	}
	for _, bad := range []string{"", "!!!", "e30", "bm90LWpzb24"} {
		if _, err := parseJoinCode(bad); err == nil {
			t.Errorf("%q hätte scheitern müssen", bad)
		}
	}
}

func TestJoinCodesRedeem(t *testing.T) {
	var jc joinCodes
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	sec, _ := jc.issue("node", now)
	if role, err := jc.redeem(sec, "1.2.3.4", now); err != nil || role != "node" {
		t.Fatalf("%q %v", role, err)
	}
	if _, err := jc.redeem(sec, "1.2.3.4", now); err == nil || err.Error() != "Beitrittscode bereits benutzt" {
		t.Fatalf("zweites Mal: %v", err)
	}

	sec, _ = jc.issue("witness", now)
	if _, err := jc.redeem(sec, "1.2.3.4", now.Add(31*time.Minute)); err == nil || err.Error() != "Beitrittscode abgelaufen" {
		t.Fatalf("abgelaufen: %v", err)
	}

	sec, _ = jc.issue("node", now)
	for i := 0; i < 5; i++ {
		if _, err := jc.redeem("falsch", "9.9.9.9", now); err == nil {
			t.Fatal("falsch akzeptiert")
		}
	}
	if _, err := jc.redeem(sec, "9.9.9.9", now.Add(10*time.Second)); err == nil || err.Error() != "Zu viele Versuche, bitte eine Minute warten" {
		t.Fatalf("Sperre: %v", err)
	}
	// andere Quelle ist nicht betroffen, der Code blieb gültig
	if role, err := jc.redeem(sec, "8.8.8.8", now.Add(10*time.Second)); err != nil || role != "node" {
		t.Fatalf("Code sollte gültig bleiben: %q %v", role, err)
	}
}

func TestJoinCodesRedeemAfterWindow(t *testing.T) {
	var jc joinCodes
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	sec, _ := jc.issue("node", now)
	for i := 0; i < 5; i++ {
		jc.redeem("falsch", "9.9.9.9", now)
	}
	if role, err := jc.redeem(sec, "9.9.9.9", now.Add(61*time.Second)); err != nil || role != "node" {
		t.Fatalf("nach der Minute: %q %v", role, err)
	}
}

func TestJoinCodesIssueBadRole(t *testing.T) {
	var jc joinCodes
	if _, err := jc.issue("boss", time.Now()); err == nil {
		t.Fatal("Rolle boss akzeptiert")
	}
}
