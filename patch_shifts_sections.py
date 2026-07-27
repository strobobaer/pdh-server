path = "web/templates/shifts.gohtml"
with open(path, encoding="utf-8") as f:
    c = f.read()

old = '''    <tbody>
    {{range .Users}}
    {{$uid := .UserID}}
    <tr>'''

new = '''    <tbody>
    <tr>
      <td colspan="{{len .Days | add1}}" style="background:var(--bg3);padding:8px 12px">
        <div style="display:flex;align-items:center;gap:10px">
          <i class="ti ti-tool" style="color:var(--accent)"></i>
          <span style="font-weight:600;font-size:12px">Schichtschlosser 1</span>
          <input type="tel" class="form-input" style="max-width:180px;font-size:12px" placeholder="Telefonnummer"
            value="{{.LocksmithSlot1Phone}}" onchange="saveLocksmithPhone(1,this.value)">
        </div>
      </td>
    </tr>
    {{range .Users}}
    {{if .ShiftLocksmith1}}
    {{$uid := .UserID}}
    <tr>'''

if old not in c:
    raise SystemExit("FEHLER: tbody-Start nicht gefunden")
c = c.replace(old, new, 1)

with open(path, "w", encoding="utf-8") as f:
    f.write(c)
print("OK: Abschnitt 'Schichtschlosser 1' eingefuegt (Teil 1).")

