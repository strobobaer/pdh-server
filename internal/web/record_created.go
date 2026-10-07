package web

import (
	"context"
	"fmt"
	"strings"
)

// Hinweis beim Anlegen (migrations/106): jeder neue Vorgang steht als
// 'created' in der Aenderungs-Warteschlange. Unabhaengig vom Weg (Neu anlegen,
// Modulseite, Leitstand, API, Copilot) gilt dann:
//   - der Ersteller bekommt von PDH-System eine Bestaetigung im Chat,
//   - Zugewiesene/Verantwortliche/Gruppenmitglieder einen Hinweis „neu für dich“,
//   - nicht zugewiesene Tickets, Stoerungen, Aufgaben und Wartungen gehen an
//     die Broker (sofern nicht schon im Leitstand verteilt).
// Aus Wartungsplaenen automatisch erzeugte Auftraege sind ausgenommen - sie
// erscheinen rechtzeitig auf der Zuweisungsseite der Broker.

// createdInfo: was zum Anlegen-Hinweis aus dem Datensatz gelesen wird.
type createdInfo struct {
	title, creator, priority, infra string
	fromPlan                        bool
}

func createdInfoQuery(module string) string {
	switch module {
	case "ticket", "task":
		return fmt.Sprintf(`SELECT r.title, COALESCE(r.created_by::text, ''), COALESCE(r.priority::text, ''), COALESCE(i.name, ''), false
			FROM %s r LEFT JOIN infrastructure i ON i.id = r.infrastructure_id WHERE r.id = $1::uuid`, recordModules[module].Table)
	case "fault":
		return `SELECT r.title, COALESCE(r.created_by::text, ''), COALESCE(r.severity::text, ''), COALESCE(i.name, ''), false
			FROM faults r LEFT JOIN infrastructure i ON i.id = r.infrastructure_id WHERE r.id = $1::uuid`
	case "maintenance_task":
		return `SELECT r.title, COALESCE(r.created_by::text, ''), COALESCE(r.priority::text, ''), COALESCE(i.name, ''), r.plan_id IS NOT NULL
			FROM maintenance_tasks r LEFT JOIN infrastructure i ON i.id = r.infrastructure_id WHERE r.id = $1::uuid`
	case "project":
		return `SELECT name, COALESCE(created_by::text, ''), '', '', false FROM projects WHERE id = $1::uuid`
	case "kvp":
		return `SELECT 'KVP-' || lpad(number::text, 4, '0') || ' · ' || title, COALESCE(created_by::text, submitter_id::text, ''), '', '', false
			FROM kvp_ideas WHERE id = $1::uuid`
	}
	return ""
}

func (h *Handler) notifyRecordCreated(ctx context.Context, module, rec string) {
	mi, ok := recordModules[module]
	q := createdInfoQuery(module)
	if !ok || q == "" {
		return
	}
	var ci createdInfo
	if err := h.db.QueryRow(ctx, q, rec).Scan(&ci.title, &ci.creator, &ci.priority, &ci.infra, &ci.fromPlan); err != nil {
		return // inzwischen geloescht
	}
	if ci.fromPlan {
		return
	}
	link := mi.Path + rec

	// Zugewiesene, Verantwortliche und Gruppenmitglieder (ohne den Ersteller)
	var assignees []string
	for _, u := range h.changeRecipients(ctx, module, rec) {
		if u != ci.creator {
			assignees = append(assignees, u)
		}
	}
	if len(assignees) > 0 {
		who := h.chatUserName(ctx, ci.creator)
		if who == "" {
			who = "PDH"
		}
		h.systemNotify(ctx, assignees, fmt.Sprintf("📌 Neu für dich: %s **%s** – angelegt von %s.\n%s", mi.Label, ci.title, who, link))
	}

	// Broker-Verteilung, falls niemand zugewiesen ist
	broker := ""
	if _, isBrokerKind := brokerKinds[module]; isBrokerKind && ci.creator != "" {
		if done, names := h.brokerDispatched(ctx, module, rec); done {
			broker = names
		} else {
			broker = h.dispatchToBrokers(ctx, module, rec, ci.title, ci.priority, ci.infra, ci.creator)
		}
	}

	// Bestaetigung an den Ersteller
	if ci.creator == "" || !h.wantsSystemHints(ctx, ci.creator) {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "✅ Angelegt: %s **%s**", mi.Label, ci.title)
	if names := h.chatNames(ctx, assignees); len(names) > 0 {
		fmt.Fprintf(&b, "\nZuständig: %s", chatNameList(names))
	} else if broker != "" {
		fmt.Fprintf(&b, "\nOhne Zuweisung – %s", broker)
	}
	fmt.Fprintf(&b, "\n%s", link)
	h.systemNotify(ctx, []string{ci.creator}, b.String())
}

// brokerDispatched: wurde der Vorgang schon an die Broker verteilt (Leitstand)?
// Liefert dann die Rueckmeldung wie dispatchToBrokers.
func (h *Handler) brokerDispatched(ctx context.Context, module, rec string) (bool, string) {
	var msg, names string
	if err := h.db.QueryRow(ctx, `
		SELECT COALESCE(message, ''), COALESCE(new_value, '') FROM record_history
		 WHERE ref_type = $1 AND ref_id = $2::uuid AND action = 'broker' ORDER BY created_at DESC LIMIT 1`,
		module, rec).Scan(&msg, &names); err != nil {
		return false, ""
	}
	if names != "" {
		return true, msg + ": " + names
	}
	return true, msg
}

// wantsSystemHints: aktiver, echter Benutzer mit eingeschalteten Hinweisen.
func (h *Handler) wantsSystemHints(ctx context.Context, userID string) bool {
	var ok bool
	_ = h.db.QueryRow(ctx, `SELECT active AND NOT is_bot AND change_notifications FROM users WHERE id = $1::uuid`, userID).Scan(&ok)
	return ok
}
