package web

import (
	"context"
	"testing"
)

// Der Status von pdh-ha trägt je Server die Netzwerk-Zusammenfassung; ein
// älterer Partner ohne das Feld bleibt nil („unbekannt“).
func TestHANodeNetDecodes(t *testing.T) {
	f := &fakeHA{events: `[]`, status: `{"ok":true,"configured":true,"mode":"duo",
 "self":{"id":"pdh1","kind":"node","role":"aktiv","seen":true,"paths":[],
   "net":{"if":"eth0","addr":"192.0.2.17/24","mode":"static","gw":"192.0.2.1","dns":["192.0.2.1","1.1.1.1"],"np":true}},
 "peers":[{"id":"pdh2","kind":"node","role":"passiv","seen":true,"paths":[],
   "net":{"if":"eth0","addr":"192.0.2.14/24","mode":"dhcp","v6":["2a02::14"]}},
  {"id":"alt","kind":"node","role":"passiv","seen":true,"paths":[]}]}`}
	startFakeHA(t, f)
	h := &Handler{}
	var st haStatus
	if err := h.haDo(context.Background(), "GET", "/v1/status", nil, &st); err != nil {
		t.Fatal(err)
	}
	if st.Self.Net == nil || st.Self.Net.Addr != "192.0.2.17/24" || !st.Self.Net.Netplan || len(st.Self.Net.DNS) != 2 {
		t.Fatalf("self %+v", st.Self.Net)
	}
	if len(st.Peers) != 2 || st.Peers[0].Net == nil || st.Peers[0].Net.Addr != "192.0.2.14/24" || st.Peers[0].Net.Mode != "dhcp" {
		t.Fatalf("peers %+v", st.Peers)
	}
	if st.Peers[1].Net != nil {
		t.Fatalf("älterer Partner ohne Feld: %+v", st.Peers[1].Net)
	}
}
