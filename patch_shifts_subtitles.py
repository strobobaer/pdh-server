path = "web/templates/shifts.gohtml"
with open(path, encoding="utf-8") as f:
    c = f.read()

old = '''          {{if .ShiftLocksmith2}}<div style="display:flex;align-items:center;gap:3px;font-size:10px;color:var(--muted)"><i class="ti ti-tool"></i> Schlosser 2{{if .LocksmithPhone}} {{.LocksmithPhone}}{{end}}</div>{{end}}
          <div style="display:flex;gap:4px">
            {{if .Sharpening}}<i class="ti ti-scissors" style="font-size:11px;color:var(--accent)" title="Schärferei"></i>{{end}}
            {{if .HeatingFill}}<i class="ti ti-flame" style="font-size:11px;color:var(--accent)" title="Heizungsbefüllung"></i>{{end}}
            {{if .ShiftLeader}}<i class="ti ti-star" style="font-size:11px;color:var(--amber)" title="Schichtleiter"></i>{{end}}
          </div>'''

new = '''          {{if .ShiftLocksmith2}}<div style="display:flex;align-items:center;gap:3px;font-size:10px;color:var(--muted)"><i class="ti ti-tool"></i> Schlosser 2{{if .LocksmithPhone}} {{.LocksmithPhone}}{{end}}</div>{{end}}
          {{if .Sharpening}}<div style="display:flex;align-items:center;gap:3px;font-size:10px;color:var(--accent)"><i class="ti ti-scissors"></i> Schärferei</div>{{end}}
          {{if .HeatingFill}}<div style="display:flex;align-items:center;gap:3px;font-size:10px;color:var(--accent)"><i class="ti ti-flame"></i> Heizungsbefüllung</div>{{end}}
          <div style="display:flex;gap:4px">
            {{if .ShiftLeader}}<i class="ti ti-star" style="font-size:11px;color:var(--amber)" title="Schichtleiter"></i>{{end}}
          </div>'''

if old not in c:
    raise SystemExit("FEHLER: Untertitel-Block nicht gefunden")
c = c.replace(old, new, 1)

with open(path, "w", encoding="utf-8") as f:
    f.write(c)
print("OK: Sharpening/HeatingFill als Untertitel-Zeilen umgestellt.")

