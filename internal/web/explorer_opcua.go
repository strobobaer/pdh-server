package web

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
)

// OPC-UA-Explorer: Knotenbaum eines Servers erkunden (eine Ebene je Aufklappen), Werte,
// Datentyp, Qualität und Zugriffsrechte ansehen, Knoten live verfolgen und testweise
// schreiben. Rahmen (Rechte, Sitzungsgrenze, Rückfrage, Verlauf) steht in explorer.go.
// Werte stammen aus einem fremden Server: sie werden nur als Text weitergegeben,
// bereinigt und in der Länge begrenzt.

const (
	opcuaExplorerMaxChildren = 500
	opcuaExplorerMaxWatch    = 20
	opcuaExplorerMaxValue    = 200  // Zeichen je Wert
	opcuaExplorerMaxName     = 200  // Zeichen je Name
	opcuaExplorerMaxNodeID   = 500  // Zeichen einer NodeID
	opcuaExplorerMaxWrite    = 4000 // Zeichen eines zu schreibenden Werts
	opcuaExplorerMaxBytes    = 32   // angezeigte Bytes einer ByteString
	opcuaExplorerReadChunk   = 1000 // Leseaufträge je Read-Aufruf
	opcuaExplorerOpTimeout   = 20 * time.Second
	opcuaExplorerMinTick     = 250
	opcuaExplorerMaxTick     = 10000
	opcuaExplorerDefTick     = 1000
)

// OPCUANodeInfo beschreibt einen Knoten für die Anzeige.
type OPCUANodeInfo struct {
	NodeID      string `json:"nodeid"`
	Name        string `json:"name"`
	Class       string `json:"class"`
	HasChildren bool   `json:"has_children"`
	Value       string `json:"value"`
	DataType    string `json:"datatype"`
	Timestamp   string `json:"timestamp"`
	Status      string `json:"status"`
	Access      string `json:"access"`
	Writable    bool   `json:"writable"`
}

// OPCUAWatchValue ist ein neuer Wert eines verfolgten Knotens.
type OPCUAWatchValue struct {
	NodeID    string `json:"nodeid"`
	Value     string `json:"value"`
	Timestamp string `json:"timestamp"`
	Status    string `json:"status"`
}

// ── Werte aufbereiten ────────────────────────────────────────

// opcuaCleanText macht fremden Text anzeigesicher: gültiges UTF-8, keine Steuerzeichen,
// höchstens max Zeichen (sonst mit „…“).
func opcuaCleanText(s string, max int) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	if utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max]) + "…"
	}
	return s
}

// opcuaFormatValue gibt den Wert eines Variants als Text aus. Nicht endliche Zahlen
// („NaN“, „+Inf“) sind Text; Byte-Folgen erscheinen als Hex, lange Werte werden gekürzt.
func opcuaFormatValue(v *ua.Variant) string {
	if v == nil {
		return ""
	}
	val := v.Value()
	switch x := val.(type) {
	case nil:
		return ""
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(x), 'g', -1, 32)
	case string:
		return opcuaCleanText(x, opcuaExplorerMaxValue)
	case []byte:
		n := len(x)
		if n > opcuaExplorerMaxBytes {
			n = opcuaExplorerMaxBytes
		}
		s := "0x" + hex.EncodeToString(x[:n])
		if len(x) > n {
			s += "…"
		}
		return s
	case time.Time:
		return x.Local().Format("02.01.2006 15:04:05")
	case *ua.LocalizedText:
		if x == nil {
			return ""
		}
		return opcuaCleanText(x.Text, opcuaExplorerMaxValue)
	case *ua.QualifiedName:
		if x == nil {
			return ""
		}
		return opcuaCleanText(x.Name, opcuaExplorerMaxValue)
	case *ua.NodeID:
		if x == nil {
			return ""
		}
		return opcuaCleanText(x.String(), opcuaExplorerMaxValue)
	}
	if rv := reflect.ValueOf(val); rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
		// Felder: Elemente einzeln, aber nur so viele, wie in die Länge passen.
		parts := make([]string, 0, 16)
		total := 0
		for i := 0; i < rv.Len(); i++ {
			e := opcuaCleanText(fmt.Sprint(rv.Index(i).Interface()), opcuaExplorerMaxValue)
			total += len(e) + 1
			if total > 4*opcuaExplorerMaxValue {
				parts = append(parts, "…")
				break
			}
			parts = append(parts, e)
		}
		return opcuaCleanText("["+strings.Join(parts, " ")+"]", opcuaExplorerMaxValue)
	}
	return opcuaCleanText(fmt.Sprint(val), opcuaExplorerMaxValue)
}

func opcuaStatusText(s ua.StatusCode) string {
	if s == ua.StatusOK {
		return "Good"
	}
	return opcuaCleanText(s.Error(), opcuaExplorerMaxName)
}

func opcuaTimestamp(dv *ua.DataValue) string {
	t := dv.SourceTimestamp
	if t.IsZero() {
		t = dv.ServerTimestamp
	}
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("02.01.2006 15:04:05")
}

func opcuaClassName(c ua.NodeClass) string {
	switch c {
	case ua.NodeClassObject:
		return "Object"
	case ua.NodeClassVariable:
		return "Variable"
	}
	return "Other"
}

// opcuaDataTypeName: Name des Datentyps (z. B. „Double“), sonst die NodeID als Text.
func opcuaDataTypeName(v *ua.Variant) string {
	if v == nil {
		return ""
	}
	nid, ok := v.Value().(*ua.NodeID)
	if !ok || nid == nil {
		return ""
	}
	if nid.Namespace() == 0 && nid.Type() == ua.NodeIDTypeNumeric {
		if n := id.Name(nid.IntID()); n != "" {
			return n
		}
	}
	return opcuaCleanText(nid.String(), opcuaExplorerMaxName)
}

// opcuaAccess wertet die Zugriffsstufe aus (Bit 1 lesen, Bit 2 schreiben).
func opcuaAccess(level *ua.DataValue) (text string, writable, ok bool) {
	if level == nil || level.Status != ua.StatusOK || level.Value == nil {
		return "", false, false
	}
	var b uint8
	switch x := level.Value.Value().(type) {
	case uint8:
		b = x
	case ua.AccessLevelType:
		b = uint8(x)
	default:
		return "", false, false
	}
	r, w := b&1 != 0, b&2 != 0
	switch {
	case r && w:
		return "lesen/schreiben", true, true
	case w:
		return "schreiben", true, true
	case r:
		return "lesen", false, true
	}
	return "kein Zugriff", false, true
}

// opcuaFillVariable trägt Wert, Datentyp, Zeit, Qualität und Zugriff aus den gelesenen
// Attributen in info ein.
func opcuaFillVariable(info *OPCUANodeInfo, value, dataType, userLevel, level *ua.DataValue) {
	if value != nil {
		info.Status = opcuaStatusText(value.Status)
		if value.Status == ua.StatusOK {
			info.Value = opcuaFormatValue(value.Value)
		}
		info.Timestamp = opcuaTimestamp(value)
	}
	if dataType != nil && dataType.Status == ua.StatusOK {
		info.DataType = opcuaDataTypeName(dataType.Value)
	}
	if text, w, ok := opcuaAccess(userLevel); ok {
		info.Access, info.Writable = text, w
	} else if text, w, ok := opcuaAccess(level); ok {
		info.Access, info.Writable = text, w
	}
}

func opcuaParseNodeID(s string) (*ua.NodeID, error) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > opcuaExplorerMaxNodeID || !opcuaNodeIDRe.MatchString(s) {
		return nil, errors.New("Ungültige NodeID")
	}
	n, err := ua.ParseNodeID(s)
	if err != nil {
		return nil, errors.New("Ungültige NodeID")
	}
	return n, nil
}

// opcuaReadAttrs liest die Attribute in Stücken; Ergebnis je Auftrag, Reihenfolge wie rvs.
func opcuaReadAttrs(ctx context.Context, c *opcua.Client, rvs []*ua.ReadValueID) ([]*ua.DataValue, error) {
	out := make([]*ua.DataValue, 0, len(rvs))
	for start := 0; start < len(rvs); start += opcuaExplorerReadChunk {
		end := start + opcuaExplorerReadChunk
		if end > len(rvs) {
			end = len(rvs)
		}
		resp, err := c.Read(ctx, &ua.ReadRequest{
			MaxAge:             0,
			TimestampsToReturn: ua.TimestampsToReturnBoth,
			NodesToRead:        rvs[start:end],
		})
		if err != nil {
			return nil, err
		}
		if len(resp.Results) != end-start {
			return nil, errors.New("Unerwartete Antwort des Servers")
		}
		out = append(out, resp.Results...)
	}
	return out, nil
}

func opcuaRV(n *ua.NodeID, a ua.AttributeID) *ua.ReadValueID {
	return &ua.ReadValueID{NodeID: n, AttributeID: a, DataEncoding: &ua.QualifiedName{}}
}

// ── Baum ─────────────────────────────────────────────────────

// opcuaBrowseChildren liefert die Kinder eines Knotens (eine Ebene, hierarchische
// Verweise, nur Objekte und Variablen); nodeID "" = Objects-Ordner. Die Werte der
// Variablen kommen mit einem Read-Aufruf. Zweiter Wert: auf limit abgeschnitten.
func opcuaBrowseChildren(ctx context.Context, c *opcua.Client, nodeID string, limit int) ([]OPCUANodeInfo, bool, error) {
	parent := ua.NewNumericNodeID(0, id.ObjectsFolder)
	if strings.TrimSpace(nodeID) != "" {
		p, err := opcuaParseNodeID(nodeID)
		if err != nil {
			return nil, false, err
		}
		parent = p
	}
	if limit <= 0 {
		limit = opcuaExplorerMaxChildren
	}
	resp, err := c.Browse(ctx, &ua.BrowseRequest{
		View:                          &ua.ViewDescription{ViewID: ua.NewTwoByteNodeID(0)},
		RequestedMaxReferencesPerNode: uint32(limit + 1),
		NodesToBrowse: []*ua.BrowseDescription{{
			NodeID:          parent,
			BrowseDirection: ua.BrowseDirectionForward,
			ReferenceTypeID: ua.NewNumericNodeID(0, id.HierarchicalReferences),
			IncludeSubtypes: true,
			NodeClassMask:   uint32(ua.NodeClassObject | ua.NodeClassVariable),
			ResultMask:      uint32(ua.BrowseResultMaskAll),
		}},
	})
	if err != nil {
		return nil, false, err
	}
	if len(resp.Results) != 1 {
		return nil, false, errors.New("Unerwartete Antwort des Servers")
	}
	res := resp.Results[0]
	if res.StatusCode != ua.StatusOK {
		return nil, false, fmt.Errorf("Der Server lehnt das Auflisten ab: %s", opcuaStatusText(res.StatusCode))
	}
	refs := res.References
	cp := res.ContinuationPoint
	for len(cp) > 0 && len(refs) <= limit {
		next, err := c.BrowseNext(ctx, &ua.BrowseNextRequest{ContinuationPoints: [][]byte{cp}})
		if err != nil || len(next.Results) != 1 {
			cp = nil
			break
		}
		refs = append(refs, next.Results[0].References...)
		cp = next.Results[0].ContinuationPoint
	}
	if len(cp) > 0 { // nicht ausgeschöpften Fortsetzungspunkt freigeben
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		_, _ = c.BrowseNext(rctx, &ua.BrowseNextRequest{ReleaseContinuationPoints: true, ContinuationPoints: [][]byte{cp}})
		cancel()
	}
	truncated := len(refs) > limit
	if truncated {
		refs = refs[:limit]
	}

	nodes := make([]OPCUANodeInfo, 0, len(refs))
	var vars []int // Indizes der Variablen in nodes
	var rvs []*ua.ReadValueID
	for _, ref := range refs {
		if ref == nil || ref.NodeID == nil || ref.NodeID.NodeID == nil {
			continue
		}
		nid := ref.NodeID.NodeID
		info := OPCUANodeInfo{NodeID: opcuaCleanText(nid.String(), opcuaExplorerMaxNodeID), Class: opcuaClassName(ref.NodeClass)}
		if ref.DisplayName != nil && ref.DisplayName.Text != "" {
			info.Name = opcuaCleanText(ref.DisplayName.Text, opcuaExplorerMaxName)
		} else if ref.BrowseName != nil && ref.BrowseName.Name != "" {
			info.Name = opcuaCleanText(ref.BrowseName.Name, opcuaExplorerMaxName)
		} else {
			info.Name = info.NodeID
		}
		switch ref.NodeClass {
		case ua.NodeClassObject:
			info.HasChildren = true
		case ua.NodeClassVariable:
			vars = append(vars, len(nodes))
			rvs = append(rvs,
				opcuaRV(nid, ua.AttributeIDValue), opcuaRV(nid, ua.AttributeIDDataType),
				opcuaRV(nid, ua.AttributeIDUserAccessLevel), opcuaRV(nid, ua.AttributeIDAccessLevel))
		}
		nodes = append(nodes, info)
	}
	if len(rvs) > 0 {
		vals, err := opcuaReadAttrs(ctx, c, rvs)
		if err != nil {
			return nil, false, err
		}
		for i, idx := range vars {
			opcuaFillVariable(&nodes[idx], vals[4*i], vals[4*i+1], vals[4*i+2], vals[4*i+3])
		}
	}
	return nodes, truncated, nil
}

// opcuaReadNode liest einen einzelnen Knoten mit allen Anzeigeangaben.
func opcuaReadNode(ctx context.Context, c *opcua.Client, nodeID string) (OPCUANodeInfo, error) {
	nid, err := opcuaParseNodeID(nodeID)
	if err != nil {
		return OPCUANodeInfo{}, err
	}
	vals, err := opcuaReadAttrs(ctx, c, []*ua.ReadValueID{
		opcuaRV(nid, ua.AttributeIDNodeClass), opcuaRV(nid, ua.AttributeIDDisplayName), opcuaRV(nid, ua.AttributeIDBrowseName),
		opcuaRV(nid, ua.AttributeIDValue), opcuaRV(nid, ua.AttributeIDDataType),
		opcuaRV(nid, ua.AttributeIDUserAccessLevel), opcuaRV(nid, ua.AttributeIDAccessLevel),
	})
	if err != nil {
		return OPCUANodeInfo{}, err
	}
	if vals[0].Status != ua.StatusOK || vals[0].Value == nil {
		return OPCUANodeInfo{}, fmt.Errorf("Knoten nicht lesbar: %s", opcuaStatusText(vals[0].Status))
	}
	class := ua.NodeClass(0)
	switch x := vals[0].Value.Value().(type) {
	case int32:
		class = ua.NodeClass(x)
	case uint32:
		class = ua.NodeClass(x)
	}
	info := OPCUANodeInfo{NodeID: opcuaCleanText(nid.String(), opcuaExplorerMaxNodeID), Class: opcuaClassName(class)}
	info.Name = info.NodeID
	if vals[1].Status == ua.StatusOK && vals[1].Value != nil {
		if lt, ok := vals[1].Value.Value().(*ua.LocalizedText); ok && lt != nil && lt.Text != "" {
			info.Name = opcuaCleanText(lt.Text, opcuaExplorerMaxName)
		}
	}
	if info.Name == info.NodeID && vals[2].Status == ua.StatusOK && vals[2].Value != nil {
		if qn, ok := vals[2].Value.Value().(*ua.QualifiedName); ok && qn != nil && qn.Name != "" {
			info.Name = opcuaCleanText(qn.Name, opcuaExplorerMaxName)
		}
	}
	switch class {
	case ua.NodeClassObject:
		info.HasChildren = true
	case ua.NodeClassVariable:
		opcuaFillVariable(&info, vals[3], vals[4], vals[5], vals[6])
	}
	return info, nil
}

// ── Schreiben ────────────────────────────────────────────────

// opcuaWriteValue schreibt einen Wert in den Datentyp, den der Knoten gerade trägt
// (deutsche Dezimalzahlen bei Gleitkommatypen), und prüft das Ergebnis des Servers.
func opcuaWriteValue(ctx context.Context, c *opcua.Client, nodeID, value string) error {
	nid, err := opcuaParseNodeID(nodeID)
	if err != nil {
		return err
	}
	vals, err := opcuaReadAttrs(ctx, c, []*ua.ReadValueID{opcuaRV(nid, ua.AttributeIDValue)})
	if err != nil {
		return fmt.Errorf("Knoten konnte nicht gelesen werden: %w", err)
	}
	cur := vals[0]
	if cur.Status != ua.StatusOK || cur.Value == nil {
		return fmt.Errorf("Knoten konnte nicht gelesen werden: %s", opcuaStatusText(cur.Status))
	}
	switch cur.Value.Value().(type) {
	case float32, float64:
		value = explorerDecimal(value)
	case int16, int32, int64, uint16, uint32, bool:
		value = strings.TrimSpace(value)
	case string: // Text bleibt unverändert
	default:
		return fmt.Errorf("Der Datentyp %T wird zum Testschreiben nicht unterstützt", cur.Value.Value())
	}
	variant, err := coerceOPCUAVariant(cur.Value, value)
	if err != nil {
		return fmt.Errorf("Der Wert passt nicht zum Datentyp des Knotens: %w", err)
	}
	resp, err := c.Write(ctx, &ua.WriteRequest{NodesToWrite: []*ua.WriteValue{{
		NodeID:      nid,
		AttributeID: ua.AttributeIDValue,
		Value:       &ua.DataValue{EncodingMask: ua.DataValueValue, Value: variant},
	}}})
	if err != nil {
		return fmt.Errorf("Schreiben fehlgeschlagen: %w", err)
	}
	if len(resp.Results) == 0 {
		return errors.New("Schreiben fehlgeschlagen: keine Antwort des Servers")
	}
	if resp.Results[0] != ua.StatusOK {
		return fmt.Errorf("Der Server lehnt das Schreiben ab: %s", opcuaStatusText(resp.Results[0]))
	}
	return nil
}

// ── Live verfolgen ───────────────────────────────────────────

// opcuaWatch meldet Wertänderungen der Knoten über eine Subscription mit Monitored Items.
// Lehnt der Server Subscriptions ab, wird im selben Takt zyklisch gelesen. Kehrt erst
// zurück, wenn ctx beendet ist oder ein Fehler auftritt; es startet keine eigenen Goroutinen.
func opcuaWatch(ctx context.Context, c *opcua.Client, nodeIDs []string, interval time.Duration, emit func(OPCUAWatchValue)) error {
	ids := make([]*ua.NodeID, 0, len(nodeIDs))
	for _, s := range nodeIDs {
		n, err := opcuaParseNodeID(s)
		if err != nil {
			return err
		}
		ids = append(ids, n)
	}
	if len(ids) == 0 {
		return errors.New("Kein Knoten angegeben")
	}
	notify := make(chan *opcua.PublishNotificationData, 32)
	sub, err := c.Subscribe(ctx, &opcua.SubscriptionParameters{Interval: interval}, notify)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return opcuaWatchPolling(ctx, c, ids, interval, emit)
	}
	cancelSub := func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = sub.Cancel(cctx)
	}
	reqs := make([]*ua.MonitoredItemCreateRequest, len(ids))
	for i, n := range ids {
		reqs[i] = opcua.NewMonitoredItemCreateRequestWithDefaults(n, ua.AttributeIDValue, uint32(i))
		reqs[i].RequestedParameters.SamplingInterval = float64(interval / time.Millisecond)
	}
	mon, err := sub.Monitor(ctx, ua.TimestampsToReturnBoth, reqs...)
	if err != nil || mon == nil || len(mon.Results) != len(ids) {
		cancelSub()
		if ctx.Err() != nil {
			return nil
		}
		return opcuaWatchPolling(ctx, c, ids, interval, emit)
	}
	defer cancelSub()
	for i, r := range mon.Results {
		if r.StatusCode != ua.StatusOK {
			emit(OPCUAWatchValue{NodeID: opcuaCleanText(ids[i].String(), opcuaExplorerMaxNodeID), Status: opcuaStatusText(r.StatusCode)})
		}
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg := <-notify:
			if msg == nil {
				continue
			}
			if msg.Error != nil {
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("Verbindung zum OPC-UA-Server gestört: %w", msg.Error)
			}
			if dc, ok := msg.Value.(*ua.DataChangeNotification); ok {
				for _, item := range dc.MonitoredItems {
					if item == nil || item.Value == nil || int(item.ClientHandle) >= len(ids) {
						continue
					}
					emit(opcuaWatchFrom(ids[item.ClientHandle], item.Value))
				}
			}
		}
	}
}

func opcuaWatchFrom(n *ua.NodeID, dv *ua.DataValue) OPCUAWatchValue {
	w := OPCUAWatchValue{NodeID: opcuaCleanText(n.String(), opcuaExplorerMaxNodeID), Status: opcuaStatusText(dv.Status), Timestamp: opcuaTimestamp(dv)}
	if dv.Status == ua.StatusOK {
		w.Value = opcuaFormatValue(dv.Value)
	}
	return w
}

// opcuaWatchPolling liest die Knoten im Takt interval (Rückfall ohne Subscription).
func opcuaWatchPolling(ctx context.Context, c *opcua.Client, ids []*ua.NodeID, interval time.Duration, emit func(OPCUAWatchValue)) error {
	rvs := make([]*ua.ReadValueID, len(ids))
	for i, n := range ids {
		rvs[i] = opcuaRV(n, ua.AttributeIDValue)
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		rctx, cancel := context.WithTimeout(ctx, opcuaExplorerOpTimeout)
		vals, err := opcuaReadAttrs(rctx, c, rvs)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("Lesen fehlgeschlagen: %w", err)
		}
		for i, dv := range vals {
			emit(opcuaWatchFrom(ids[i], dv))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// ── Handler ──────────────────────────────────────────────────

// opcuaConnect öffnet die Verbindung; Fehler enthalten die Meldung für den Anwender.
func opcuaConnect(ctx context.Context, c explorerConn) (*opcua.Client, error) {
	return openOPCUAClient(ctx, c.Config)
}

func opcuaClose(cl *opcua.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = cl.Close(ctx)
}

func writeOPCUAJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// opcuaReadGuard: Rechte, Verbindung und Typ (404 JSON, wenn keine OPC-UA-Verbindung).
func (h *Handler) opcuaReadGuard(w http.ResponseWriter, r *http.Request, direction string) (explorerConn, bool) {
	c, ok := h.explorerReadGuard(w, r, direction)
	if !ok {
		return c, false
	}
	if c.Kind != "opcua" {
		writeGlobalBoardError(w, http.StatusNotFound, "Keine OPC-UA-Verbindung")
		return c, false
	}
	return c, true
}

// OPCUAExplorerChildren: GET …/explorer/opcua/children?node= – Kinder eines Knotens.
func (h *Handler) OPCUAExplorerChildren(direction string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := h.opcuaReadGuard(w, r, direction)
		if !ok {
			return
		}
		node := strings.TrimSpace(r.URL.Query().Get("node"))
		if node != "" {
			if _, err := opcuaParseNodeID(node); err != nil {
				writeGlobalBoardError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), opcuaExplorerOpTimeout)
		defer cancel()
		cl, err := opcuaConnect(ctx, c)
		if err != nil {
			writeGlobalBoardError(w, http.StatusBadGateway, err.Error())
			return
		}
		defer opcuaClose(cl)
		nodes, truncated, err := opcuaBrowseChildren(ctx, cl, node, opcuaExplorerMaxChildren)
		if err != nil {
			writeGlobalBoardError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeOPCUAJSON(w, map[string]any{"nodes": nodes, "truncated": truncated})
	}
}

// OPCUAExplorerNode: GET …/explorer/opcua/node?node= – ein Knoten mit Details.
func (h *Handler) OPCUAExplorerNode(direction string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := h.opcuaReadGuard(w, r, direction)
		if !ok {
			return
		}
		node := strings.TrimSpace(r.URL.Query().Get("node"))
		if _, err := opcuaParseNodeID(node); err != nil {
			writeGlobalBoardError(w, http.StatusBadRequest, err.Error())
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), opcuaExplorerOpTimeout)
		defer cancel()
		cl, err := opcuaConnect(ctx, c)
		if err != nil {
			writeGlobalBoardError(w, http.StatusBadGateway, err.Error())
			return
		}
		defer opcuaClose(cl)
		info, err := opcuaReadNode(ctx, cl, node)
		if err != nil {
			writeGlobalBoardError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeOPCUAJSON(w, info)
	}
}

// OPCUAExplorerWatch: GET …/explorer/opcua/watch?node=…&interval= – Live-Werte (SSE „value“).
func (h *Handler) OPCUAExplorerWatch(direction string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := h.opcuaReadGuard(w, r, direction)
		if !ok {
			return
		}
		var nodes []string
		seen := map[string]bool{}
		for _, n := range r.URL.Query()["node"] {
			n = strings.TrimSpace(n)
			if n == "" || seen[n] {
				continue
			}
			if _, err := opcuaParseNodeID(n); err != nil {
				writeGlobalBoardError(w, http.StatusBadRequest, err.Error())
				return
			}
			seen[n] = true
			nodes = append(nodes, n)
		}
		if len(nodes) == 0 {
			writeGlobalBoardError(w, http.StatusBadRequest, "Bitte mindestens einen Knoten angeben")
			return
		}
		if len(nodes) > opcuaExplorerMaxWatch {
			writeGlobalBoardError(w, http.StatusBadRequest, fmt.Sprintf("Höchstens %d Knoten gleichzeitig verfolgen", opcuaExplorerMaxWatch))
			return
		}
		tick := opcuaExplorerDefTick
		if s := strings.TrimSpace(r.URL.Query().Get("interval")); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil {
				writeGlobalBoardError(w, http.StatusBadRequest, "Ungültiger Takt")
				return
			}
			tick = int(math.Min(math.Max(float64(n), opcuaExplorerMinTick), opcuaExplorerMaxTick))
		}
		h.runExplorerStream(w, r, func(ctx context.Context, send func(string, any)) error {
			cl, err := opcuaConnect(ctx, c)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			defer opcuaClose(cl)
			send("connected", map[string]any{"endpoint": opcuaCleanText(strings.TrimSpace(c.Config["endpoint_url"]), opcuaExplorerMaxName), "nodes": len(nodes), "interval": tick})
			return opcuaWatch(ctx, cl, nodes, time.Duration(tick)*time.Millisecond, func(v OPCUAWatchValue) {
				send("value", v)
			})
		})
	}
}

// OPCUAExplorerWrite: POST …/explorer/opcua/write (node, value, confirm) – testweise schreiben.
func (h *Handler) OPCUAExplorerWrite(direction string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 4*opcuaExplorerMaxWrite)
		if err := r.ParseForm(); err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				writeGlobalBoardError(w, http.StatusRequestEntityTooLarge, "Der Wert ist zu groß")
			} else {
				writeGlobalBoardError(w, http.StatusBadRequest, "Ungültige Eingabe")
			}
			return
		}
		c, ok := h.explorerWriteGuard(w, r, direction)
		if !ok {
			return
		}
		if c.Kind != "opcua" {
			writeGlobalBoardError(w, http.StatusNotFound, "Keine OPC-UA-Verbindung")
			return
		}
		node := strings.TrimSpace(r.FormValue("node"))
		value := r.FormValue("value")
		if _, err := opcuaParseNodeID(node); err != nil {
			writeGlobalBoardError(w, http.StatusBadRequest, err.Error())
			return
		}
		if utf8.RuneCountInString(value) > opcuaExplorerMaxWrite {
			writeGlobalBoardError(w, http.StatusBadRequest, "Der Wert ist zu lang")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), opcuaExplorerOpTimeout)
		defer cancel()
		// Geschrieben wird – auch bei Fehlschlag – mit Eintrag im Verlauf, mit Ergebnis und Datentyp.
		var dataType string
		var after OPCUANodeInfo
		writeErr := func() error {
			cl, err := opcuaConnect(ctx, c)
			if err != nil {
				return err
			}
			defer opcuaClose(cl)
			if before, err := opcuaReadNode(ctx, cl, node); err == nil {
				dataType = before.DataType
			}
			if err := opcuaWriteValue(ctx, cl, node, value); err != nil {
				return err
			}
			// Der neue Wert kommt vom Server zurück; schlägt das Nachlesen fehl, ist trotzdem geschrieben.
			after, _ = opcuaReadNode(ctx, cl, node)
			return nil
		}()
		logged := opcuaCleanText(value, opcuaExplorerMaxValue)
		if dataType != "" {
			logged += " (Datentyp " + dataType + ")"
		}
		if writeErr != nil {
			reason := []rune(opcuaCleanText(writeErr.Error(), 80))
			logged = "Fehlgeschlagen (" + string(reason) + ") – " + logged
		}
		h.explorerLogWrite(r, c, node, logged)
		if writeErr != nil {
			writeGlobalBoardError(w, http.StatusBadGateway, opcuaCleanText(writeErr.Error(), 300))
			return
		}
		if after.NodeID == "" {
			after = OPCUANodeInfo{NodeID: node, DataType: dataType}
		}
		writeOPCUAJSON(w, after)
	}
}
