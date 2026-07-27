path = "web/templates/shifts.gohtml"
with open(path, encoding="utf-8") as f:
    c = f.read()

anchor = "function openShiftPlanModal(startTab){"
if anchor not in c:
    raise SystemExit("FEHLER: Anker nicht gefunden")

addition = '''async function saveLocksmithPhone(slot, phone){
  try{
    var teamIdField = slot === 1 ? '{{.LocksmithSlot1TeamID}}' : '{{.LocksmithSlot2TeamID}}';
    if(!teamIdField){
      alert('Fuer diesen Slot ist noch kein Team hinterlegt. Bitte zuerst unter "Verwalten" ein Team anlegen und zuweisen.');
      return;
    }
    var res = await fetch('/api/v1/shifts/teams/'+teamIdField, {
      method:'POST', credentials:'same-origin', headers:{'Content-Type':'application/json'},
      body: JSON.stringify({name:'', phone:phone})
    });
    if(!res.ok){
      var json = await res.json().catch(function(){return null;});
      throw new Error((json && json.error) || ('HTTP '+res.status));
    }
  }catch(e){ alert('Fehler beim Speichern der Telefonnummer: '+e.message); }
}

''' + anchor

c = c.replace(anchor, addition, 1)

with open(path, "w", encoding="utf-8") as f:
    f.write(c)
print("OK: saveLocksmithPhone() ergaenzt.")


