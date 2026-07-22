path = "internal/modules/maintenance/checklist_templates.go"
with open(path, encoding="utf-8") as f:
    c = f.read()

broken = '''_, err := r.db.Exec(ctx, `+"`"+`UPDATE maintenance_checklist_template_items SET label=$1, description=$2, item_type=$3, required=$4, interval_days=$5, sort_order=$6 WHERE id=$7`+"`"+`,'''

fixed = '''_, err := r.db.Exec(ctx, `UPDATE maintenance_checklist_template_items SET label=$1, description=$2, item_type=$3, required=$4, interval_days=$5, sort_order=$6 WHERE id=$7`,'''

if broken not in c:
    raise SystemExit("FEHLER: kaputte Zeile nicht gefunden - zeig mir Zeile 155-165 der Datei")

c = c.replace(broken, fixed, 1)

with open(path, "w", encoding="utf-8") as f:
    f.write(c)
print("OK: Backtick-Fehler behoben.")


