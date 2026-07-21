#!/usr/bin/env python3
"""
Patcht internal/web/handler.go:
1. TicketStatusWeb(): prueft jetzt den Fehler von UpdateStatus() (vorher
   stillschweigend ignoriert) und zeigt ihn an, statt so zu tun als waere
   der Status gewechselt worden
2. Route /tickets/{id}/status-web: PUT -> POST (war vermutlich seit jeher
   ueber Cloudflare/Nginx blockiert)
3. Neue Funktion TicketResolve() + Route POST /tickets/{id}/resolve-web
4. RecordArchiveWeb(): "ticket"-Fall ruft jetzt QuickResolve() statt
   UpdateStatus(..., StatusClosed, ...) auf, da UpdateStatus() den direkten
   Sprung zu "closed" jetzt ablehnt

Aufruf:
    python3 apply_ticket_routine_web_patch.py internal/web/handler.go
"""
import sys

REPLACEMENTS = [
    (
        "Route status-web: PUT -> POST",
        '''	r.Put("/tickets/{id}/status-web", h.TicketStatusWeb)''',
        '''	r.Post("/tickets/{id}/status-web", h.TicketStatusWeb) // FIX: war PUT, wird von Cloudflare/Nginx blockiert
	r.Post("/tickets/{id}/resolve-web", h.TicketResolve)''',
    ),
    (
        "TicketStatusWeb: Fehlerbehandlung + neue TicketResolve-Funktion",
        '''func (h *Handler) TicketStatusWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	r.ParseForm()
	status := tickets.Status(r.FormValue("status"))
	u := getUser(r)
	h.tickets.UpdateStatus(r.Context(), id, status, u.ID)
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<span class="badge %s">%s</span>`, statusClass(string(status)), statusLabel(string(status)))
}''',
        '''func (h *Handler) TicketStatusWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	r.ParseForm()
	status := tickets.Status(r.FormValue("status"))
	u := getUser(r)
	w.Header().Set("Content-Type", "text/html")
	if err := h.tickets.UpdateStatus(r.Context(), id, status, u.ID); err != nil {
		fmt.Fprintf(w, `<span class="badge b-red" title="%s">Nicht möglich</span>`, esc(err.Error()))
		return
	}
	fmt.Fprintf(w, `<span class="badge %s">%s</span>`, statusClass(string(status)), statusLabel(string(status)))
}

func (h *Handler) TicketResolve(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	r.ParseForm()
	u := getUser(r)
	noPartsNeeded := r.FormValue("no_parts_needed") == "on"
	w.Header().Set("Content-Type", "text/html")
	if err := h.tickets.Resolve(r.Context(), id, r.FormValue("resolution"), r.FormValue("root_cause"), u.ID, noPartsNeeded); err != nil {
		fmt.Fprintf(w, `<div style="color:var(--red);padding:10px;text-align:center;font-size:13px">%s</div>`, esc(err.Error()))
		return
	}
	fmt.Fprintf(w, `<div style="color:var(--green);padding:12px;text-align:center"><i class="ti ti-check"></i> Ticket gelöst! <a href="/tickets" style="color:var(--accent)">Zurück zur Liste</a></div>`)
}''',
    ),
    (
        "RecordArchiveWeb: ticket-Fall nutzt QuickResolve",
        '''	case "ticket":
		err = h.tickets.UpdateStatus(r.Context(), id, tickets.StatusClosed, u.ID)''',
        '''	case "ticket":
		err = h.tickets.QuickResolve(r.Context(), id, "Archiviert", "", u.ID)''',
    ),
]


def main():
    if len(sys.argv) != 2:
        print("Aufruf: python3 apply_ticket_routine_web_patch.py internal/web/handler.go")
        sys.exit(1)

    path = sys.argv[1]
    with open(path, "r", encoding="utf-8") as f:
        content = f.read()

    changed = content
    for label, old, new in REPLACEMENTS:
        count = changed.count(old)
        if count == 0:
            print(f"FEHLER: Block '{label}' wurde nicht gefunden. Nichts geändert.")
            sys.exit(1)
        if count > 1:
            print(f"FEHLER: Block '{label}' wurde {count}x gefunden (erwartet 1x). Nichts geändert.")
            sys.exit(1)
        changed = changed.replace(old, new, 1)

    with open(path, "w", encoding="utf-8") as f:
        f.write(changed)
    print(f"OK: {path} gepatcht ({len(REPLACEMENTS)} Stellen ersetzt).")


if __name__ == "__main__":
    main()
