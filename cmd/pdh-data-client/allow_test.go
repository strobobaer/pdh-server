package main

import (
	"bytes"
	"context"
	"log"
	"net"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"pdh/internal/datanet"
	"pdh/internal/datanet/nodetest"
)

func TestAllowLogsRejectionOncePerMinutePerAddress(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	f := &nodetest.Fake{Tags: map[string][]string{
		"100.64.0.5": {"tag:pdh-test1"},
		"100.64.0.6": {datanet.TagPDH},
	}}
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	allow := newAllow(context.Background(), f, func() time.Time { return now })
	addr := func(s string) net.Addr { return net.TCPAddrFromAddrPort(netip.MustParseAddrPort(s)) }

	if allow(addr("100.64.0.5:40000")) {
		t.Fatal("falscher Tag wurde zugelassen")
	}
	want := "Verbindung von 100.64.0.5 abgewiesen: Tags tag:pdh-test1, erwartet tag:pdh"
	if !strings.Contains(buf.String(), want) {
		t.Fatalf("Protokoll fehlt %q:\n%s", want, buf.String())
	}
	n := strings.Count(buf.String(), "abgewiesen")
	now = now.Add(30 * time.Second)
	allow(addr("100.64.0.5:40001"))
	if got := strings.Count(buf.String(), "abgewiesen"); got != n {
		t.Errorf("innerhalb einer Minute erneut protokolliert (%d statt %d)", got, n)
	}
	allow(addr("100.64.0.7:40001")) // andere Adresse: eigener Eintrag
	if got := strings.Count(buf.String(), "abgewiesen"); got != n+1 {
		t.Errorf("andere Adresse nicht protokolliert (%d)", got)
	}
	now = now.Add(31 * time.Second)
	allow(addr("100.64.0.5:40002"))
	if got := strings.Count(buf.String(), "abgewiesen"); got != n+2 {
		t.Errorf("nach einer Minute nicht erneut protokolliert (%d)", got)
	}
	before := buf.Len()
	if !allow(addr("100.64.0.6:1")) {
		t.Error("tag:pdh wurde abgewiesen")
	}
	if buf.Len() != before {
		t.Error("Zulassung wurde protokolliert")
	}
}
