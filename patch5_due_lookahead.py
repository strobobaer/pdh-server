path = "internal/modules/maintenance/maintenance.go"
with open(path, encoding="utf-8") as f:
    c = f.read()

old = "WHERE mt.due_date::date <= NOW()::date AND mt.status IN ('open','in_progress')"
new = "WHERE mt.due_date::date <= (NOW() + INTERVAL '2 days')::date AND mt.status IN ('open','in_progress')"

if old not in c:
    raise SystemExit("FEHLER: Stelle nicht gefunden")

c = c.replace(old, new, 1)

with open(path, "w", encoding="utf-8") as f:
    f.write(c)
print("OK: " + path + " gepatcht (2 Tage Vorlauf fuer faellige Auftraege).")


