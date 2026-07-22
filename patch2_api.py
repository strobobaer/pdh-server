path = "internal/modules/maintenance/checklist_api.go"
with open(path, encoding="utf-8") as f:
    c = f.read()

# ── Service-Methode ergänzen ──
sig_a = "func (s *Service) DeleteChecklistTemplateItemForAPI(r *http.Request, itemID string) error {"
if sig_a not in c:
    raise SystemExit("FEHLER: Signatur a nicht gefunden")
idx = c.index(sig_a)
brace_start = c.index("{", idx)
depth = 0
i = brace_start
while i < len(c):
    if c[i] == "{":
        depth += 1
    elif c[i] == "}":
        depth -= 1
        if depth == 0:
            break
    i += 1
end_a = i + 1

addition_a = '''

func (s *Service) UpdateChecklistTemplateItemForAPI(r *http.Request, itemID string, in createChecklistTemplateItemInput) (*ChecklistTemplateItem, error) {
	item := &ChecklistTemplateItem{
		Label: strings.TrimSpace(in.Label), Description: strings.TrimSpace(in.Description),
		ItemType: in.ItemType, Required: in.Required, IntervalDays: in.IntervalDays, SortOrder: in.SortOrder,
	}
	if item.ItemType == "" { item.ItemType = "checkbox" }
	if item.IntervalDays <= 0 { item.IntervalDays = 1 }
	if item.SortOrder == 0 { item.SortOrder = 100 }
	if err := s.repo.UpdateChecklistTemplateItem(r.Context(), itemID, item); err != nil { return nil, err }
	item.ID = itemID
	return item, nil
}'''

c = c[:end_a] + addition_a + c[end_a:]

# ── Route ergänzen ──
route_anchor = 'r.Delete("/items/{itemID}", h.DeleteChecklistTemplateItem)'
if route_anchor not in c:
    raise SystemExit("FEHLER: Route-Anker nicht gefunden")
c = c.replace(route_anchor, route_anchor + '\n\tr.Post("/items/{itemID}", h.UpdateChecklistTemplateItem)', 1)

# ── Handler-Funktion ergänzen ──
sig_b = "func (h *Handler) DeleteChecklistTemplateItem(w http.ResponseWriter, r *http.Request) {"
if sig_b not in c:
    raise SystemExit("FEHLER: Signatur b nicht gefunden")
idx_b = c.index(sig_b)
brace_start_b = c.index("{", idx_b)
depth = 0
i = brace_start_b
while i < len(c):
    if c[i] == "{":
        depth += 1
    elif c[i] == "}":
        depth -= 1
        if depth == 0:
            break
    i += 1
end_b = i + 1

addition_b = '''

func (h *Handler) UpdateChecklistTemplateItem(w http.ResponseWriter, r *http.Request) {
	var in createChecklistTemplateItemInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil { response.Error(w, 400, "ungueltige eingabe"); return }
	if strings.TrimSpace(in.Label) == "" { response.Error(w, 400, "label ist pflicht"); return }
	if in.IntervalDays <= 0 { response.Error(w, 400, "intervall ist pflicht"); return }
	item, err := h.svc.UpdateChecklistTemplateItemForAPI(r, chi.URLParam(r, "itemID"), in)
	if err != nil { response.Error(w, 500, err.Error()); return }
	response.JSON(w, 200, item)
}'''

c = c[:end_b] + addition_b + c[end_b:]

with open(path, "w", encoding="utf-8") as f:
    f.write(c)
print("OK: " + path + " gepatcht.")


