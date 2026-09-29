package web

import (
	"strings"
	"testing"
	"time"
)

func TestChatHubDeliveryAndPresence(t *testing.T) {
	hub := newChatHub()
	events := make(chan string, 10)
	hub.onPresence = func(uid string, online bool) {
		if online {
			events <- uid + ":on"
		} else {
			events <- uid + ":off"
		}
	}
	a1 := hub.subscribe("a")
	a2 := hub.subscribe("a") // zweiter Tab: kein erneutes "online"
	b := hub.subscribe("b")
	if got := <-events; got != "a:on" {
		t.Fatalf("presence: %s", got)
	}
	if got := <-events; got != "b:on" {
		t.Fatalf("presence: %s", got)
	}
	hub.send([]string{"a"}, chatEvent{Type: "message"})
	if (<-a1).Type != "message" || (<-a2).Type != "message" {
		t.Fatal("beide Tabs von a müssen die Nachricht erhalten")
	}
	select {
	case <-b:
		t.Fatal("b darf die Nachricht nicht erhalten")
	default:
	}
	// Ein Tab schliessen: weiterhin online
	hub.unsubscribe("a", a1)
	if !hub.isOnline("a") {
		t.Fatal("a ist mit zweitem Tab noch online")
	}
	// Letzten Tab schliessen: waehrend der Karenzzeit noch online, kein offline-Event
	hub.unsubscribe("a", a2)
	if !hub.isOnline("a") {
		t.Fatal("während der Karenzzeit gilt a als online")
	}
	// Reconnect innerhalb der Karenzzeit -> weder offline noch erneut online
	a3 := hub.subscribe("a")
	select {
	case ev := <-events:
		t.Fatalf("unerwartetes Presence-Event beim Reconnect: %s", ev)
	case <-time.After(50 * time.Millisecond):
	}
	hub.unsubscribe("a", a3)
	hub.unsubscribe("b", b)
}

func TestChatHubDoesNotBlockOnFullBuffer(t *testing.T) {
	hub := newChatHub()
	_ = hub.subscribe("slow")
	done := make(chan struct{})
	go func() {
		for i := 0; i < 500; i++ {
			hub.send([]string{"slow"}, chatEvent{Type: "x"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("send blockiert bei vollem Puffer")
	}
}

func TestChatExtractLinks(t *testing.T) {
	id := "0f8fad5b-d9cb-469f-a165-70867728950e"
	body := "Bitte ansehen: https://pdh.example.com/faults/" + id + " und /maintenance/tasks/" + strings.ToUpper(id) +
		" sowie nochmal /faults/" + id + " und /unbekannt/" + id
	links := chatExtractLinks(body)
	if len(links) != 2 {
		t.Fatalf("erwartet 2 eindeutige Links, got %v", links)
	}
	if links[0][0] != "faults" || links[1][0] != "maintenance/tasks" || links[1][1] != id {
		t.Errorf("links: %v", links)
	}
	for _, l := range links {
		if _, ok := chatLinkTypeByPath(l[0]); !ok {
			t.Errorf("kein Typ für %s", l[0])
		}
	}
}

func TestInitialsAndNames(t *testing.T) {
	if initials("Özlem", "müller") != "ÖM" || initials("", "") != "?" {
		t.Error("initials")
	}
	if chatNameList([]string{"A", "B", "C"}) != "A, B und C" || chatNameList([]string{"A"}) != "A" {
		t.Error("chatNameList")
	}
	if urlPathEscape("Bericht Ä 1.pdf") != "Bericht%20%C3%84%201.pdf" {
		t.Errorf("urlPathEscape: %s", urlPathEscape("Bericht Ä 1.pdf"))
	}
}

func TestChatPageRenders(t *testing.T) {
	tmpl := loadTestTemplates(t)
	out := renderPage(t, tmpl, "chat", ChatPageData{
		BaseData: BaseData{Page: "chat", CanChat: true}, MeID: "me-1",
		ShareURL: "/tickets/1", ShareTitle: `Ticket "A" <b>`, QuickEmojis: chatQuickEmojis,
	})
	for _, want := range []string{`data-me="me-1"`, "new EventSource('/chat/stream')", "window.Chat =", `data-share-title="Ticket &#34;A&#34; &lt;b&gt;"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Chat-Seite enthält %q nicht", want)
		}
	}
	// Auf der Chat-Seite selbst kein globaler Zaehler-Stream und kein Teilen-Button
	if strings.Contains(out, "pdhShareToChat()\" title") {
		t.Error("Teilen-Button auf der Chat-Seite")
	}
	other := renderPage(t, tmpl, "purchase_report", PurchaseReportData{BaseData: BaseData{Page: "directory", CanChat: true}})
	if !strings.Contains(other, "chat-nav-badge") || !strings.Contains(other, `data-context-panel="chat"`) || !strings.Contains(other, "pdhSideChat") || !strings.Contains(other, "Im Chat teilen") {
		t.Error("Chat-Einbindung (Zähler/Teilen) fehlt auf anderen Seiten")
	}
	noChat := renderPage(t, tmpl, "purchase_report", PurchaseReportData{})
	if strings.Contains(noChat, `id="sc-body"`) || strings.Contains(noChat, `data-context-panel="chat"`) || strings.Contains(noChat, "new EventSource") {
		t.Error("ohne Berechtigung keine Chat-Einbindung")
	}
}
