package extsync

import (
	"context"
	"math"
	"testing"
)

func TestLoadOverloadedReasons(t *testing.T) {
	ok := Load{CPUPerCore: 0.5, MemAvailable: 0.5, DBUsed: 0.5, Known: true}
	cases := []struct {
		name   string
		l      Load
		over   bool
		reason string
	}{
		{"alles im Rahmen", ok, false, ""},
		{"Prozessor genau an der Schwelle", Load{CPUPerCore: 0.85, MemAvailable: 0.5, DBUsed: 0.5, Known: true}, false, ""},
		{"Prozessor über 0,85", Load{CPUPerCore: 0.86, MemAvailable: 0.5, DBUsed: 0.5, Known: true}, true, LoadReasonCPU},
		{"Arbeitsspeicher genau 10 %", Load{CPUPerCore: 0.5, MemAvailable: 0.10, DBUsed: 0.5, Known: true}, false, ""},
		{"Arbeitsspeicher unter 10 %", Load{CPUPerCore: 0.5, MemAvailable: 0.09, DBUsed: 0.5, Known: true}, true, LoadReasonMem},
		{"Datenbank genau 80 %", Load{CPUPerCore: 0.5, MemAvailable: 0.5, DBUsed: 0.80, Known: true}, false, ""},
		{"Datenbank über 80 %", Load{CPUPerCore: 0.5, MemAvailable: 0.5, DBUsed: 0.81, Known: true}, true, LoadReasonDB},
		{"ohne Messwert nie überlastet", Load{CPUPerCore: 5, MemAvailable: 0, DBUsed: 1, Known: false}, false, ""},
	}
	for _, c := range cases {
		over, reason := c.l.Overloaded()
		if over != c.over || reason != c.reason {
			t.Errorf("%s: (%v, %q), erwartet (%v, %q)", c.name, over, reason, c.over, c.reason)
		}
	}
	if LoadReasonCPU != "Prozessor" || LoadReasonMem != "Arbeitsspeicher" || LoadReasonDB != "Datenbank" {
		t.Error("Gründe müssen Prozessor, Arbeitsspeicher und Datenbank heißen")
	}
}

func TestParseLoadAvgAndMemInfo(t *testing.T) {
	l1, err := parseLoadAvg("1.70 0.80 0.50 2/345 6789\n")
	if err != nil || l1 != 1.70 {
		t.Fatalf("loadavg: %v %v", l1, err)
	}
	if _, err := parseLoadAvg(""); err == nil {
		t.Error("leere loadavg muss ein Fehler sein")
	}
	mem := "MemTotal:       16000000 kB\nMemFree:          500000 kB\nMemAvailable:    1600000 kB\n"
	avail, err := parseMemInfo(mem)
	if err != nil || math.Abs(avail-0.10) > 1e-9 {
		t.Fatalf("meminfo: %v %v", avail, err)
	}
	if _, err := parseMemInfo("MemTotal: 100 kB\n"); err == nil {
		t.Error("fehlendes MemAvailable muss ein Fehler sein")
	}
}

func TestSystemLoadWithoutPool(t *testing.T) {
	// Ohne Pool und auf Systemen ohne /proc darf nichts abstürzen.
	l := SystemLoad(nil)(context.Background())
	if l.DBUsed != 0 {
		t.Errorf("ohne Pool keine Datenbanklast: %+v", l)
	}
}
