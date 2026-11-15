package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/gopcua/opcua/ua"
)

func TestOPCUAReadChunked(t *testing.T) {
	rvs := make([]*ua.ReadValueID, 250)
	for i := range rvs {
		rvs[i] = &ua.ReadValueID{AttributeID: ua.AttributeID(i + 1)}
	}
	var sizes []int
	limit := 30
	read := func(part []*ua.ReadValueID) ([]*ua.DataValue, error) {
		sizes = append(sizes, len(part))
		if len(part) > limit {
			return nil, ua.StatusBadTooManyOperations
		}
		out := make([]*ua.DataValue, len(part))
		for i, p := range part {
			out[i] = &ua.DataValue{Status: ua.StatusCode(p.AttributeID)}
		}
		return out, nil
	}
	got, err := opcuaReadChunked(rvs, 100, read)
	if err != nil || len(got) != 250 {
		t.Fatalf("%d Ergebnisse, %v", len(got), err)
	}
	for i, dv := range got {
		if dv.Status != ua.StatusCode(i+1) {
			t.Fatalf("Reihenfolge bei %d", i)
		}
	}
	if sizes[0] != 100 || sizes[1] != 50 || sizes[2] != 25 {
		t.Errorf("Halbieren: %v", sizes)
	}
	for _, n := range sizes[3:] {
		if n > 25 {
			t.Errorf("Stückgröße blieb zu groß: %v", sizes)
		}
	}
	// Der Server lehnt sogar ein einzelnes Stück ab: Fehler statt Endlosschleife.
	limit = 0
	if _, err := opcuaReadChunked(rvs, 100, read); err == nil {
		t.Error("kein Fehler bei Stückgröße 1")
	}
	// Andere Fehler werden nicht wiederholt.
	calls := 0
	_, err = opcuaReadChunked(rvs, 100, func([]*ua.ReadValueID) ([]*ua.DataValue, error) {
		calls++
		return nil, ua.StatusBadTimeout
	})
	if err == nil || calls != 1 {
		t.Errorf("anderer Fehler: %d Aufrufe, %v", calls, err)
	}
}

func TestOPCUAConnGate(t *testing.T) {
	g := &opcuaGate{n: map[string]int{}, max: 2}
	r1, ok1 := g.acquire("a")
	_, ok2 := g.acquire("a")
	_, ok3 := g.acquire("a")
	if !ok1 || !ok2 || ok3 {
		t.Fatalf("Grenze: %v %v %v", ok1, ok2, ok3)
	}
	if _, ok := g.acquire("b"); !ok {
		t.Error("andere Verbindung blockiert")
	}
	r1()
	r1() // mehrfaches Freigeben zählt nur einmal
	if _, ok := g.acquire("a"); !ok {
		t.Error("nach Freigabe kein Platz")
	}
	if _, ok := g.acquire("a"); ok {
		t.Error("doppelte Freigabe hat Platz erzeugt")
	}
}

func TestOPCUAExplorerBusy429(t *testing.T) {
	endpoint, _, ns := startTestOPCUAServer(t)
	connID := uuid.NewString()
	h := &Handler{
		hasPermHook: func(_ *http.Request, p string) bool { return p == "import.execute" || p == "import.write" },
		explorerLoadHook: func(_ *http.Request, dir string) (explorerConn, error) {
			return explorerConn{Direction: dir, ID: connID, Name: "M", Kind: "opcua", Config: map[string]string{"endpoint_url": endpoint}}, nil
		},
	}
	r1, _ := opcuaConnGate.acquire(connID)
	r2, _ := opcuaConnGate.acquire(connID)
	nodeID := fmt.Sprintf("ns=%d;s=Temperatur", ns)
	check := func(name string, w *httptest.ResponseRecorder) {
		if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "Gerät ist gerade beschäftigt – kurz warten") {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	h.OPCUAExplorerChildren("import")(w, explorerReq("GET", "/x?node="))
	check("children", w)
	w = httptest.NewRecorder()
	h.OPCUAExplorerNode("import")(w, explorerReq("GET", "/x?node="+url.QueryEscape(nodeID)))
	check("node", w)
	w = httptest.NewRecorder()
	h.OPCUAExplorerWatch("import")(w, explorerReq("GET", "/x?node="+url.QueryEscape(nodeID)))
	check("watch", w)
	w = httptest.NewRecorder()
	h.OPCUAExplorerWrite("import")(w, opcuaWriteReq(nodeID, "1", nil))
	check("write", w)
	r1()
	r2()
	w = httptest.NewRecorder()
	h.OPCUAExplorerChildren("import")(w, explorerReq("GET", "/x?node="))
	if w.Code != http.StatusOK {
		t.Errorf("nach Freigabe: %d %s", w.Code, w.Body.String())
	}
	opcuaConnGate.mu.Lock()
	_, held := opcuaConnGate.n[connID]
	opcuaConnGate.mu.Unlock()
	if held {
		t.Error("Platz nach der Anfrage nicht freigegeben")
	}
}

// Mehr als 500 Kinder: Antwort abgeschnitten, die Werte werden in Stücken gelesen.
func TestOPCUAExplorerChildrenTruncated(t *testing.T) {
	endpoint, _, ns := startTestOPCUAServerN(t, 520)
	h := opcuaTestHandler(endpoint, map[string]bool{"import.execute": true})
	w := httptest.NewRecorder()
	h.OPCUAExplorerChildren("import")(w, explorerReq("GET", "/x?node="+url.QueryEscape(fmt.Sprintf("ns=%d;i=85", ns))))
	var out struct {
		Nodes     []OPCUANodeInfo `json:"nodes"`
		Truncated bool            `json:"truncated"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if !out.Truncated || len(out.Nodes) != opcuaExplorerMaxChildren {
		t.Fatalf("abgeschnitten=%v, %d Knoten", out.Truncated, len(out.Nodes))
	}
	if last := out.Nodes[len(out.Nodes)-1]; last.Status != "Good" || last.Value == "" || last.Access != "lesen" {
		t.Errorf("letzter Knoten ohne Werte: %+v", last)
	}
}

func TestOPCUAExplorerWriteSuccessAndReadOnly(t *testing.T) {
	endpoint, _, ns := startTestOPCUAServer(t)
	pool := dataNetTestPool(t)
	cid := uuid.NewString()
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM record_history WHERE ref_id = $1`, cid) })
	h := &Handler{
		db:          pool,
		hasPermHook: func(_ *http.Request, p string) bool { return p == "import.execute" || p == "import.write" },
		explorerLoadHook: func(_ *http.Request, dir string) (explorerConn, error) {
			return explorerConn{Direction: dir, ID: cid, Name: "M", Kind: "opcua", Config: map[string]string{"endpoint_url": endpoint}}, nil
		},
	}
	w := httptest.NewRecorder()
	h.OPCUAExplorerWrite("import")(w, noUserReq(opcuaWriteReq(fmt.Sprintf("ns=%d;s=Temperatur", ns), "30", nil)))
	var info OPCUANodeInfo
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &info) != nil || info.Value != "30" {
		t.Fatalf("Erfolg: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.OPCUAExplorerWrite("import")(w, noUserReq(opcuaWriteReq(fmt.Sprintf("ns=%d;s=Zaehler", ns), "1", nil)))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Knoten ist nur lesbar") {
		t.Fatalf("nur lesbar: %d %s", w.Code, w.Body.String())
	}
}
