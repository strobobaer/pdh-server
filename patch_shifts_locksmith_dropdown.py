path = "web/templates/shifts.gohtml"
with open(path, encoding="utf-8") as f:
    c = f.read()

old1 = '''          <span style="font-weight:600;font-size:12px">Schichtschlosser 1</span>
          <input type="tel" class="form-input" style="max-width:180px;font-size:12px" placeholder="Telefonnummer"
            value="{{.LocksmithSlot1Phone}}" onchange="saveLocksmithPhone(1,this.value)">'''

new1 = '''          <span style="font-weight:600;font-size:12px">Schichtschlosser 1</span>
          <select class="form-input" style="max-width:200px;font-size:12px" onchange="setLocksmithUser(1,this.value)">
            <option value="">Nicht zugewiesen</option>
            {{range $.UserOptions}}<option value="{{.ID}}" {{if eq .ID $.LocksmithSlot1UserID}}selected{{end}}>{{.Name}}</option>{{end}}
          </select>
          <input type="tel" class="form-input" style="max-width:150px;font-size:12px" placeholder="Telefonnummer"
            value="{{.LocksmithSlot1Phone}}" onchange="saveLocksmithPhone(1,this.value)">'''

if old1 not in c:
    raise SystemExit("FEHLER: Slot-1-Block nicht gefunden")
c = c.replace(old1, new1, 1)

old2 = '''          <span style="font-weight:600;font-size:12px">Schichtschlosser 2</span>
          <input type="tel" class="form-input" style="max-width:180px;font-size:12px" placeholder="Telefonnummer"
            value="{{.LocksmithSlot2Phone}}" onchange="saveLocksmithPhone(2,this.value)">'''

new2 = '''          <span style="font-weight:600;font-size:12px">Schichtschlosser 2</span>
          <select class="form-input" style="max-width:200px;font-size:12px" onchange="setLocksmithUser(2,this.value)">
            <option value="">Nicht zugewiesen</option>
            {{range $.UserOptions}}<option value="{{.ID}}" {{if eq .ID $.LocksmithSlot2UserID}}selected{{end}}>{{.Name}}</option>{{end}}
          </select>
          <input type="tel" class="form-input" style="max-width:150px;font-size:12px" placeholder="Telefonnummer"
            value="{{.LocksmithSlot2Phone}}" onchange="saveLocksmithPhone(2,this.value)">'''

if old2 not in c:
    raise SystemExit("FEHLER: Slot-2-Block nicht gefunden")
c = c.replace(old2, new2, 1)

with open(path, "w", encoding="utf-8") as f:
    f.write(c)
print("OK: Dropdowns fuer Schichtschlosser 1/2 ergaenzt.")


