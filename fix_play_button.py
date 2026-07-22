path = "web/templates/maintenance.gohtml"
with open(path, encoding="utf-8") as f:
    c = f.read()

old = '''<div class="widget-icon-toolbar compact"><a class="btn" href="/maintenance/tasks/{{.ID}}"><i class="ti ti-eye"></i></a><button type="button" class="btn" hx-post="/maintenance/tasks/{{.ID}}/start-web" hx-on::after-request="location.reload()"><i class="ti ti-player-play"></i></button><a class="btn" href="/maintenance/tasks/{{.ID}}"><i class="ti ti-check"></i></a></div>'''

new = '''<div class="widget-icon-toolbar compact"><a class="btn" href="/maintenance/tasks/{{.ID}}"><i class="ti ti-eye"></i></a><button type="button" class="btn" hx-post="/maintenance/tasks/{{.ID}}/start-web" hx-on::after-request="window.location.href='/maintenance/tasks/{{.ID}}'"><i class="ti ti-player-play"></i></button><a class="btn" href="/maintenance/tasks/{{.ID}}"><i class="ti ti-check"></i></a></div>'''

if old not in c:
    raise SystemExit("FEHLER: Play-Button-Stelle nicht gefunden - schick mir 'grep -n \"ti-player-play\" web/templates/maintenance.gohtml'")

c = c.replace(old, new, 1)

with open(path, "w", encoding="utf-8") as f:
    f.write(c)
print("OK: Play-Button leitet jetzt zur Auftragsdetailseite weiter.")


