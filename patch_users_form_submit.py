path = "web/templates/users.gohtml"
with open(path, encoding="utf-8") as f:
    c = f.read()

old_form_tag = '''<form id="edit-form" hx-put="/users/__ID__/update-web" hx-target="#user-tbody" hx-swap="innerHTML"
          hx-on::after-request="document.getElementById('edit-modal').style.display='none'">'''

new_form_tag = '''<form id="edit-form" onsubmit="return submitUserEdit(event)">'''

if old_form_tag not in c:
    raise SystemExit("FEHLER: Formular-Tag nicht gefunden")
c = c.replace(old_form_tag, new_form_tag, 1)

old_editUser = '''function editUser(id, first, last, role, dept, phone) {
  const modal = document.getElementById('edit-modal');
  const form  = document.getElementById('edit-form');
  form.setAttribute('hx-put', '/users/'+id+'/update-web');
  htmx.process(form);
  document.getElementById('ef-first').value = first;
  document.getElementById('ef-last').value  = last;'''

new_editUser = '''let currentEditUserID = null;
function editUser(id, first, last, role, dept, phone) {
  currentEditUserID = id;
  const modal = document.getElementById('edit-modal');
  document.getElementById('ef-first').value = first;
  document.getElementById('ef-last').value  = last;'''

if old_editUser not in c:
    raise SystemExit("FEHLER: editUser()-Funktion nicht gefunden")
c = c.replace(old_editUser, new_editUser, 1)

anchor = "function toggleMenu(id){"
if anchor not in c:
    raise SystemExit("FEHLER: toggleMenu-Anker nicht gefunden")

addition = '''async function submitUserEdit(event){
  event.preventDefault();
  const payload = {
    first_name: document.getElementById('ef-first').value,
    last_name: document.getElementById('ef-last').value,
    role: document.getElementById('ef-role').value,
    department: document.getElementById('ef-dept').value,
    phone: document.getElementById('ef-phone').value,
    on_call_duty: document.getElementById('ef-oncall').checked,
    sharpening: document.getElementById('ef-sharpening').checked,
    heating_fill: document.getElementById('ef-heating').checked,
    shift_leader: document.getElementById('ef-leader').checked,
  };
  try{
    const res = await fetch('/api/v1/users/'+currentEditUserID, {
      method:'POST', credentials:'same-origin', headers:{'Content-Type':'application/json'},
      body: JSON.stringify(payload)
    });
    if(!res.ok){
      const json = await res.json().catch(function(){return null;});
      throw new Error((json && json.error) || ('HTTP '+res.status));
    }
    location.reload();
  }catch(e){ alert('Fehler beim Speichern: '+e.message); }
  return false;
}

''' + anchor

c = c.replace(anchor, addition, 1)

with open(path, "w", encoding="utf-8") as f:
    f.write(c)
print("OK: Formular auf JSON-POST an funktionierende Route umgestellt.")

