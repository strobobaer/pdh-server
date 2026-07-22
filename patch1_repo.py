path = "internal/modules/maintenance/checklist_templates.go"
with open(path, encoding="utf-8") as f:
    c = f.read()

sig = "func (r *Repository) DeleteChecklistTemplateItem(ctx context.Context, itemID string) error {"
if sig not in c:
    raise SystemExit("FEHLER: Signatur nicht gefunden")
idx = c.index(sig)
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
end = i + 1

addition = '''

func (r *Repository) UpdateChecklistTemplateItem(ctx context.Context, itemID string, item *ChecklistTemplateItem) error {
	if err := r.ensureChecklistTemplateTables(ctx); err != nil { return err }
	if item.ItemType == "" { item.ItemType = "checkbox" }
	if item.SortOrder == 0 { item.SortOrder = 100 }
	_, err := r.db.Exec(ctx, `+"`"+`UPDATE maintenance_checklist_template_items SET label=$1, description=$2, item_type=$3, required=$4, interval_days=$5, sort_order=$6 WHERE id=$7`+"`"+`,
		item.Label, item.Description, item.ItemType, item.Required, item.IntervalDays, item.SortOrder, itemID)
	return err
}'''

c = c[:end] + addition + c[end:]
with open(path, "w", encoding="utf-8") as f:
    f.write(c)
print("OK: " + path + " gepatcht.")

