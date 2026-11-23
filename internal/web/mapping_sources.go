package web

import (
	"context"
	"errors"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"pdh/internal/core/mapsuggest"
)

// Quellwerte einer Import-Verbindung für Vorschläge (Formular und Copilot):
// Spalten der Abfragen (alle Arten mit Abfragen), Spalten der Excel-/CSV-Datei,
// kurzer Mitschnitt der MQTT-Themen und die Variablen eines OPC-UA-Servers.
// Fehler einer Quelle werden Hinweise – die anderen Quellen erscheinen trotzdem.

const (
	mqttSuggestWait   = 3 * time.Second
	opcuaSuggestDepth = 4
	maxSampleRunes    = 200
)

type sourceSet struct {
	Sources []mapsuggest.Source
	Hints   []string
}

func shortSample(s string) string {
	if rs := []rune(s); len(rs) > maxSampleRunes {
		return string(rs[:maxSampleRunes]) + "…"
	}
	return s
}

// querySources: je Spalte des letzten Ergebnisses einer Abfrage ein Quellwert.
func querySources(queries []connQuery) []mapsuggest.Source {
	var out []mapsuggest.Source
	for _, q := range queries {
		if q.LastResult == nil {
			continue
		}
		row := q.LastResult.rowFor(q.Spec)
		for i, col := range q.LastResult.Columns {
			s := mapsuggest.Source{Ref: queryRef(q.ID, col), Label: q.Name + " › " + col}
			if i < len(row) {
				s.Sample = shortSample(row[i])
			}
			out = append(out, s)
		}
	}
	return out
}

// tabularSources: Spalten einer Excel-/CSV-Datei (Ref = Spaltenname wie in
// ExcelRefreshValuesWeb), Wert aus der letzten Zeile.
func tabularSources(labels []string, lastRow []string) []mapsuggest.Source {
	out := make([]mapsuggest.Source, 0, len(labels))
	for i, l := range labels {
		s := mapsuggest.Source{Ref: l, Label: l}
		if i < len(lastRow) {
			s.Sample = shortSample(lastRow[i])
		}
		out = append(out, s)
	}
	return out
}

// mqttStatSources: das Thema selbst und jedes JSON-Feld darin.
func mqttStatSources(stats []MqttTopicStat) []mapsuggest.Source {
	var out []mapsuggest.Source
	for _, st := range stats {
		out = append(out, mapsuggest.Source{Ref: mqttSourceRef(st.Topic, ""), Label: st.Topic, Sample: shortSample(st.Last)})
		for _, f := range st.Fields {
			out = append(out, mapsuggest.Source{Ref: mqttSourceRef(st.Topic, f.Path), Label: st.Topic + " › " + f.Path, Sample: shortSample(f.Value)})
		}
	}
	return out
}

// filterSources: ohne schon zugeordnete Refs, jede Ref einmal, höchstens max.
func filterSources(src []mapsuggest.Source, mapped map[string]bool, max int) []mapsuggest.Source {
	seen := map[string]bool{}
	var out []mapsuggest.Source
	for _, s := range src {
		if s.Ref == "" || mapped[s.Ref] || seen[s.Ref] {
			continue
		}
		seen[s.Ref] = true
		out = append(out, s)
		if len(out) >= max {
			break
		}
	}
	return out
}

// sampleMqttTopics hört kurz am Broker mit (eigene Client-ID, wie der Explorer)
// und liefert die gesehenen Themen samt JSON-Feldern.
func sampleMqttTopics(ctx context.Context, config map[string]string, wait time.Duration, max int) ([]MqttTopicStat, error) {
	opts, _, err := mqttClientOptions(config, "suggest")
	if err != nil {
		return nil, err
	}
	key := "ext_topic_filter"
	if config["broker_mode"] == "integrated" {
		key = "int_topic_filter"
	}
	filter := strings.TrimSpace(config[key])
	if filter == "" || mqttValidFilter(filter) != nil {
		filter = "#"
	}
	table := newMqttTopicTable(max, 1)
	c := mqtt.NewClient(opts)
	tok := c.Connect()
	if !tok.WaitTimeout(mqttExplorerConnect + time.Second) {
		return nil, errors.New("Zeitüberschreitung beim Verbinden")
	}
	if err := tok.Error(); err != nil {
		return nil, err
	}
	defer c.Disconnect(100)
	sub := c.Subscribe(filter, 0, func(_ mqtt.Client, m mqtt.Message) {
		table.add(m.Topic(), m.Payload(), m.Retained(), m.Qos(), time.Now())
	})
	if !sub.WaitTimeout(5 * time.Second) {
		return nil, errors.New("Abonnieren dauerte zu lange")
	}
	if err := sub.Error(); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
	case <-time.After(wait):
	}
	return table.takeChanged(), nil
}

// opcuaSources durchsucht den Server ab dem Objects-Ordner (Breitensuche,
// höchstens 4 Ebenen) und liefert die Variablen mit Pfad und Wert.
func opcuaSources(ctx context.Context, config map[string]string, max int) ([]mapsuggest.Source, error) {
	c, err := openOPCUAClient(ctx, config)
	if err != nil {
		return nil, err
	}
	defer c.Close(context.Background())
	type node struct{ id, path string }
	level := []node{{"", ""}}
	var out []mapsuggest.Source
	for depth := 0; depth < opcuaSuggestDepth && len(level) > 0 && len(out) < max; depth++ {
		var next []node
		for _, n := range level {
			if ctx.Err() != nil || len(out) >= max {
				break
			}
			kids, _, err := opcuaBrowseChildren(ctx, c, n.id, 200)
			if err != nil {
				if n.id == "" {
					return nil, err
				}
				continue
			}
			for _, k := range kids {
				path := k.Name
				if n.path != "" {
					path = n.path + " › " + k.Name
				}
				if k.Class == "Variable" {
					out = append(out, mapsuggest.Source{Ref: k.NodeID, Label: path, Sample: shortSample(k.Value)})
				}
				if k.HasChildren && !strings.HasPrefix(k.NodeID, "i=") { // Namensraum 0 (Server-Objekt) auslassen
					next = append(next, node{k.NodeID, path})
				}
			}
		}
		level = next
	}
	if len(out) > max {
		out = out[:max]
	}
	return out, nil
}

// mappingSources sammelt die offenen Quellwerte einer Import-Verbindung.
func (h *Handler) mappingSources(ctx context.Context, connID, kind string, config map[string]string) sourceSet {
	var set sourceSet
	var all []mapsuggest.Source
	if queryKindSupported(kind) {
		if qs, err := h.loadConnQueries(ctx, connID); err != nil {
			set.Hints = append(set.Hints, "Abfragen nicht lesbar: "+err.Error())
		} else {
			all = append(all, querySources(qs)...)
		}
	}
	switch kind {
	case "excel", "csv":
		cols, rows, err := readTabularPreview(kind, strings.TrimSpace(config["source_path"]), strings.TrimSpace(config["sheet_name"]),
			strings.TrimSpace(config["delimiter"]), config["has_header"] == "true")
		if err != nil {
			set.Hints = append(set.Hints, "Datei nicht lesbar: "+err.Error())
			break
		}
		labels := make([]string, len(cols))
		var last []string
		if len(rows) > 0 {
			last = make([]string, len(cols))
			for i, c := range cols {
				if c.Index < len(rows[len(rows)-1]) {
					last[i] = rows[len(rows)-1][c.Index]
				}
			}
		}
		for i, c := range cols {
			labels[i] = c.Label
		}
		all = append(all, tabularSources(labels, last)...)
	case "mqtt":
		stats, err := sampleMqttTopics(ctx, config, mqttSuggestWait, maxBulkMappings)
		switch {
		case err != nil:
			set.Hints = append(set.Hints, "Broker nicht erreichbar: "+err.Error())
		case len(stats) == 0:
			set.Hints = append(set.Hints, "In 3 Sekunden kam keine Nachricht – später noch einmal versuchen.")
		default:
			all = append(all, mqttStatSources(stats)...)
		}
	case "opcua":
		src, err := opcuaSources(ctx, config, maxBulkMappings)
		if err != nil {
			set.Hints = append(set.Hints, "OPC-UA-Server nicht erreichbar: "+err.Error())
		}
		all = append(all, src...)
	case "modbus":
		if len(all) == 0 {
			set.Hints = append(set.Hints, "Für Modbus erst eine Abfrage anlegen und ausführen – oder den Copilot nach der Registertabelle fragen.")
		}
	}
	mapped := map[string]bool{}
	if ms, err := h.importMappings(ctx, connID); err == nil {
		for _, m := range ms {
			mapped[m.SourceRef] = true
		}
	}
	set.Sources = filterSources(all, mapped, maxBulkMappings)
	if len(set.Sources) == maxBulkMappings {
		set.Hints = append(set.Hints, "Es werden höchstens 500 Quellwerte gezeigt.")
	}
	return set
}

// assetList: alle sichtbaren Anlagen (ausgeblendete und was darunter hängt
// fehlen) mit Pfad („Werk › Halle 2 › Presse 3“).
func (h *Handler) assetList(ctx context.Context) ([]mapsuggest.Asset, error) {
	rows, err := h.db.Query(ctx, `
		WITH RECURSIVE tree AS (
			SELECT id, name::text AS name, name::text AS path FROM infrastructure WHERE parent_id IS NULL AND hidden_at IS NULL
			UNION ALL
			SELECT i.id, i.name::text, tree.path || ' › ' || i.name FROM infrastructure i JOIN tree ON i.parent_id = tree.id
			WHERE i.hidden_at IS NULL
		)
		SELECT id::text, name, path FROM tree ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []mapsuggest.Asset
	for rows.Next() {
		var a mapsuggest.Asset
		if err := rows.Scan(&a.ID, &a.Name, &a.Path); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
