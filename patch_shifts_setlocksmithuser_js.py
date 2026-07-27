path = "web/templates/shifts.gohtml"
with open(path, encoding="utf-8") as f:
    c = f.read()

anchor = "async function saveLocksmithPhone(slot, phone){"
if anchor not in c:
    raise SystemExit("FEHLER: Anker nicht gefunden")

addition = '''async function setLocksmithUser(slot, userID){
  try{
    var res = await fetch('/api/v1/users/set-locksmith/'+slot, {
      method:'POST', credentials:'same-origin', headers:{'Content-Type':'application/json'},
      body: JSON.stringify({user_id:userID})
    });
    if(!res.ok){
      var json = await res.json().catch(function(){return null;});
      throw new Error((json && json.error) || ('HTTP '+res.status));
    }
    location.reload();
  }catch(e){ alert('Fehler beim Zuweisen: '+e.message); }
}

''' + anchor

c = c.replace(anchor, addition, 1)

with open(path, "w", encoding="utf-8") as f:
    f.write(c)
print("OK: setLocksmithUser() ergaenzt.")


