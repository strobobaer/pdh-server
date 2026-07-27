path = "internal/core/users/handler.go"
with open(path, encoding="utf-8") as f:
    c = f.read()

old_route = '''				r.Post("/{id}", h.Update) // FIX: war PUT, wird von Cloudflare/Nginx blockiert'''
new_route = old_route + '''
				r.Post("/set-locksmith/{slot}", h.SetLocksmithSlot)'''
if old_route not in c:
    raise SystemExit("FEHLER: Update-Route nicht gefunden")
c = c.replace(old_route, new_route, 1)

anchor = "func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {"
if anchor not in c:
    raise SystemExit("FEHLER: Login-Funktion nicht gefunden")

addition = '''func (h *Handler) SetLocksmithSlot(w http.ResponseWriter, r *http.Request) {
	var in struct {
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.Error(w, http.StatusBadRequest, "ungültige eingabe")
		return
	}
	slot, err := strconv.Atoi(chi.URLParam(r, "slot"))
	if err != nil || (slot != 1 && slot != 2) {
		response.Error(w, http.StatusBadRequest, "ungültiger slot (nur 1 oder 2)")
		return
	}
	if err := h.svc.SetLocksmithSlot(r.Context(), slot, in.UserID); err != nil {
		response.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "gespeichert"})
}

''' + anchor

c = c.replace(anchor, addition, 1)

# strconv-Import ergaenzen
if '"strconv"' not in c:
    c = c.replace('"net/http"', '"net/http"\n\t"strconv"', 1)

with open(path, "w", encoding="utf-8") as f:
    f.write(c)
print("OK: Handler + Route ergaenzt.")


