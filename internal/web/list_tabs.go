package web

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// Reiter fuer Listenansichten (Status + Archiv) mit Anzahl je Reiter.
// Die Listen filtern weiterhin serverseitig ueber ?status=.

type ListTab struct {
	Key, Label, URL, Icon string
	Count                 int
	Active                bool
}

type statusTabDef struct{ Key, Label, Icon string }

// statusTabs zaehlt je Status (ohne archivierte) plus "Alle" und - bei
// archivable - "Archiv". extra erlaubt zusaetzliche Query-Parameter (z. B.
// unassigned=true), die als eigener Reiter mit cond gezaehlt werden.
func (h *Handler) statusTabs(ctx context.Context, table, base, current string, defs []statusTabDef, archivable bool, extra ...listTabExtra) []ListTab {
	notArchived := "true"
	if archivable {
		notArchived = "archived_at IS NULL"
	}
	parts := []string{fmt.Sprintf("COUNT(*) FILTER (WHERE %s)", notArchived)}
	for i := range defs {
		parts = append(parts, fmt.Sprintf("COUNT(*) FILTER (WHERE %s AND status::text = $%d)", notArchived, i+1))
	}
	for _, e := range extra {
		parts = append(parts, fmt.Sprintf("COUNT(*) FILTER (WHERE %s AND %s)", notArchived, e.Cond))
	}
	if archivable {
		parts = append(parts, "COUNT(*) FILTER (WHERE archived_at IS NOT NULL)")
	}
	args := make([]interface{}, len(defs))
	for i, d := range defs {
		args[i] = d.Key
	}
	counts := make([]int, len(parts))
	dest := make([]interface{}, len(parts))
	for i := range counts {
		dest[i] = &counts[i]
	}
	_ = h.db.QueryRow(ctx, "SELECT "+strings.Join(parts, ", ")+" FROM "+table, args...).Scan(dest...)

	tabs := []ListTab{{Key: "", Label: "Alle", URL: base, Icon: "ti-list", Count: counts[0], Active: current == ""}}
	for i, d := range defs {
		tabs = append(tabs, ListTab{Key: d.Key, Label: d.Label, Icon: d.Icon, URL: base + "?status=" + url.QueryEscape(d.Key),
			Count: counts[i+1], Active: current == d.Key})
	}
	n := len(defs) + 1
	for _, e := range extra {
		tabs = append(tabs, ListTab{Key: e.Key, Label: e.Label, Icon: e.Icon, URL: base + "?" + e.Query, Count: counts[n], Active: e.Active})
		if e.Active {
			tabs[0].Active = false
		}
		n++
	}
	if archivable {
		tabs = append(tabs, ListTab{Key: "archive", Label: "Archiv", Icon: "ti-archive", URL: base + "?status=archive",
			Count: counts[n], Active: current == "archive"})
	}
	return tabs
}

type listTabExtra struct {
	Key, Label, Icon, Query, Cond string
	Active                        bool
}
