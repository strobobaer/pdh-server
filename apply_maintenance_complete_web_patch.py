#!/usr/bin/env python3
"""
Patcht internal/web/handler.go: MaintenanceTaskCompleteWeb() liest die neue
"no_parts_needed"-Checkbox und nutzt CompleteTaskValidated() statt der
unvalidierten CompleteTask(), inklusive schöner Fehleranzeige (statt
rohem http.Error-Text).

Aufruf:
    python3 apply_maintenance_complete_web_patch.py internal/web/handler.go
"""
import sys

OLD = '''func (h *Handler) MaintenanceTaskCompleteWeb(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	u := getUser(r)
	duration, _ := strconv.Atoi(r.FormValue("duration_min"))
	in := &maintenance.CompleteTaskInput{
		Notes:       r.FormValue("notes"),
		DurationMin: duration,
	}
	if err := h.maint.CompleteTask(r.Context(), chi.URLParam(r, "id"), u.ID, in); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, `<div style="color:var(--green);font-size:12px">Abgeschlossen</div>`)
}'''

NEW = '''func (h *Handler) MaintenanceTaskCompleteWeb(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	u := getUser(r)
	duration, _ := strconv.Atoi(r.FormValue("duration_min"))
	noPartsNeeded := r.FormValue("no_parts_needed") == "on"
	in := &maintenance.CompleteTaskInput{
		Notes:       r.FormValue("notes"),
		DurationMin: duration,
	}
	w.Header().Set("Content-Type", "text/html")
	if err := h.maint.CompleteTaskValidated(r.Context(), chi.URLParam(r, "id"), u.ID, in, noPartsNeeded); err != nil {
		fmt.Fprintf(w, `<div style="color:var(--red);font-size:12px">%s</div>`, esc(err.Error()))
		return
	}
	fmt.Fprint(w, `<div style="color:var(--green);font-size:12px">Abgeschlossen</div>`)
}'''


def main():
    if len(sys.argv) != 2:
        print("Aufruf: python3 apply_maintenance_complete_web_patch.py internal/web/handler.go")
        sys.exit(1)

    path = sys.argv[1]
    with open(path, "r", encoding="utf-8") as f:
        content = f.read()

    count = content.count(OLD)
    if count == 0:
        print("FEHLER: Block nicht gefunden. Nichts geändert.")
        sys.exit(1)
    if count > 1:
        print(f"FEHLER: Block {count}x gefunden (erwartet 1x). Nichts geändert.")
        sys.exit(1)

    content = content.replace(OLD, NEW, 1)
    with open(path, "w", encoding="utf-8") as f:
        f.write(content)
    print(f"OK: {path} gepatcht.")


if __name__ == "__main__":
    main()
