path = "cmd/server/main.go"
with open(path, encoding="utf-8") as f:
    c = f.read()

old = '''r.Post("/maintenance/plans/{id}/edit-web", func(w http.ResponseWriter, r *http.Request) {
                if err := r.ParseForm(); err != nil {
                        http.Error(w, "Formular konnte nicht gelesen werden", http.StatusBadRequest)
                        return
                }'''

new = '''r.Post("/maintenance/plans/{id}/edit-web", func(w http.ResponseWriter, r *http.Request) {
                if err := r.ParseMultipartForm(32 << 20); err != nil {
                        http.Error(w, "Formular konnte nicht gelesen werden", http.StatusBadRequest)
                        return
                }'''

if old not in c:
    raise SystemExit("FEHLER: Stelle nicht gefunden - zeig mir die aktuelle Version dieser Route")

c = c.replace(old, new, 1)

with open(path, "w", encoding="utf-8") as f:
    f.write(c)
print("OK: cmd/server/main.go gepatcht (ParseForm -> ParseMultipartForm).")


