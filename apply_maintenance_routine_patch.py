#!/usr/bin/env python3
"""
Patcht internal/modules/maintenance/maintenance.go:
1. CompleteTaskInput: NoPartsNeeded-Feld ergänzt
2. Handler.CompleteTask() (JSON-API): nutzt jetzt CompleteTaskValidated()
   statt der unvalidierten CompleteTask()
3. Routes(): neue Endpunkte für Maßnahmen-Verlauf + Ersatzteil-Merkliste

Aufruf:
    python3 apply_maintenance_routine_patch.py internal/modules/maintenance/maintenance.go
"""
import sys

REPLACEMENTS = [
    (
        "CompleteTaskInput: NoPartsNeeded ergaenzen",
        '''type CompleteTaskInput struct {
	Notes       string `json:"notes"`
	DurationMin int    `json:"duration_min"`
}''',
        '''type CompleteTaskInput struct {
	Notes         string `json:"notes"`
	DurationMin   int    `json:"duration_min"`
	NoPartsNeeded bool   `json:"no_parts_needed"`
}''',
    ),
    (
        "Handler.CompleteTask: validierte Methode nutzen",
        '''func (h *Handler) CompleteTask(w http.ResponseWriter, r *http.Request) {
	var in CompleteTaskInput
	decode(r, &in)
	if err := h.svc.CompleteTask(r.Context(), chi.URLParam(r, "id"), uid(r), &in); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "done"})
}''',
        '''func (h *Handler) CompleteTask(w http.ResponseWriter, r *http.Request) {
	var in CompleteTaskInput
	decode(r, &in)
	if err := h.svc.CompleteTaskValidated(r.Context(), chi.URLParam(r, "id"), uid(r), &in, in.NoPartsNeeded); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "done"})
}''',
    ),
    (
        "Routes(): neue Endpunkte ergaenzen",
        '''	r.Post("/tasks/{id}/start", h.StartTask)
	r.Post("/tasks/{id}/complete", h.CompleteTask)

	return r
}''',
        '''	r.Post("/tasks/{id}/start", h.StartTask)
	r.Post("/tasks/{id}/complete", h.CompleteTask)
	r.Post("/tasks/{id}/actions", h.AddAction)
	r.Get("/tasks/{id}/actions", h.GetActions)
	r.Delete("/tasks/{id}/actions/{actionID}", h.DeleteAction)
	r.Get("/tasks/{id}/parts-usage", h.GetPartsUsage)
	r.Post("/tasks/{id}/pending-parts", h.AddPendingPart)
	r.Get("/tasks/{id}/pending-parts", h.GetPendingParts)
	r.Delete("/tasks/{id}/pending-parts/{partItemID}", h.DeletePendingPart)

	return r
}''',
    ),
]


def main():
    if len(sys.argv) != 2:
        print("Aufruf: python3 apply_maintenance_routine_patch.py internal/modules/maintenance/maintenance.go")
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
