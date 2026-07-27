path = "web/templates/users.gohtml"
with open(path, encoding="utf-8") as f:
    c = f.read()

old = '''        <div class="form-group" style="grid-column:1/-1"><label class="form-label">Telefon</label><input id="ef-phone" name="phone" class="form-input"></div>
      </div>'''

new = '''        <div class="form-group" style="grid-column:1/-1"><label class="form-label">Telefon</label><input id="ef-phone" name="phone" class="form-input"></div>
      </div>
      <div style="border-top:1px solid var(--border);margin-top:8px;padding-top:12px">
        <div style="font-size:11px;font-weight:600;color:var(--muted);text-transform:uppercase;letter-spacing:.6px;margin-bottom:8px">Schicht-Qualifikationen</div>
        <div style="display:grid;grid-template-columns:1fr 1fr;gap:8px">
          <label style="display:flex;align-items:center;gap:6px;font-size:12px;cursor:pointer"><input type="checkbox" id="ef-oncall" name="on_call_duty"> Bereitschaft</label>
          <label style="display:flex;align-items:center;gap:6px;font-size:12px;cursor:pointer"><input type="checkbox" id="ef-sharpening" name="sharpening"> Schärferei</label>
          <label style="display:flex;align-items:center;gap:6px;font-size:12px;cursor:pointer"><input type="checkbox" id="ef-heating" name="heating_fill"> Heizungsbefüllung</label>
          <label style="display:flex;align-items:center;gap:6px;font-size:12px;cursor:pointer"><input type="checkbox" id="ef-leader" name="shift_leader"> Schichtleiter</label>
        </div>
      </div>'''

if old not in c:
    raise SystemExit("FEHLER: Telefon-Feld-Ende nicht gefunden")
c = c.replace(old, new, 1)

with open(path, "w", encoding="utf-8") as f:
    f.write(c)
print("OK: Checkboxen ergaenzt.")


