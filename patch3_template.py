path = "web/templates/maintenance.gohtml"
with open(path, encoding="utf-8") as f:
    c = f.read()

# ── 1) PUT-Bug fixen ──
old_fetch = "method:'PUT',headers:{'HX-Request':'true'},body:formData,credentials:'same-origin'});"
new_fetch = "method:'POST',headers:{'HX-Request':'true'},body:formData,credentials:'same-origin'});"
count = c.count(old_fetch)
if count == 0:
    raise SystemExit("FEHLER: PUT-Stelle nicht gefunden")
c = c.replace(old_fetch, new_fetch, 1)
print("PUT-Fix angewendet (" + str(count) + " Treffer gefunden, 1 ersetzt).")

# ── 2) renderMaintenanceChecklistTemplateList ersetzen ──
sig = "async function renderMaintenanceChecklistTemplateList(){"
if sig not in c:
    raise SystemExit("FEHLER: renderMaintenanceChecklistTemplateList nicht gefunden")
idx = c.index(sig)
brace_start = c.index("{", idx)
depth = 0
i = brace_start
while i < len(c):
    if c[i] == "{":
        depth += 1
    elif c[i] == "}":
        depth -= 1
        if depth == 0:
            break
    i += 1
end = i + 1
old_func = c[idx:end]

new_func = sig + '''
  const box = document.getElementById('maintenance-checklist-template-list');
  if(!box) return;
  if(!maintenanceChecklistTemplates.length){ box.innerHTML='Noch keine Vorlagen vorhanden'; return; }
  const blocks = [];
  for(const t of maintenanceChecklistTemplates){
    let items = [];
    try{ items = asArray(await maintenanceJSON('/api/v1/maintenance-checklists/templates/'+encodeURIComponent(t.id)+'/items')); }catch(e){ items = []; }
    const itemHtml = items.length ? items.map(i => renderMaintenanceChecklistItemRow(i)).join('') : '<div style="font-size:11px;color:var(--muted)">Keine Punkte</div>';
    blocks.push('<div style="border:1px solid var(--border);border-radius:8px;padding:10px;margin-bottom:8px;background:var(--bg3)"><div style="display:flex;align-items:center;gap:8px;margin-bottom:4px"><div style="font-weight:600;color:var(--text);flex:1">'+escapeHtml(t.name)+'</div><button type="button" class="btn btn-danger" onclick="deleteMaintenanceChecklistTemplate(\\''+escapeJs(t.id)+'\\')"><i class="ti ti-trash"></i></button></div><div style="font-size:11px;color:var(--muted);margin-bottom:6px">'+escapeHtml(t.description||'')+'</div>'+itemHtml+'</div>');
  }
  box.innerHTML = blocks.join('');
}
function renderMaintenanceChecklistItemRow(i){
  const viewId = 'mci-view-'+i.id;
  const editId = 'mci-edit-'+i.id;
  const view = '<div id="'+viewId+'" class="li"><div class="li-text"><div class="li-title" style="font-size:12px">'+escapeHtml(i.label)+'</div><div class="li-sub">'+escapeHtml(i.item_type)+' - Intervall '+escapeHtml(i.interval_days)+' Tage'+(i.required?' - Pflicht':'')+'</div></div><button type="button" class="btn" onclick="toggleMaintenanceChecklistItemEdit(\\''+escapeJs(i.id)+'\\')"><i class="ti ti-edit"></i></button><button type="button" class="btn btn-danger" onclick="deleteMaintenanceChecklistItem(\\''+escapeJs(i.id)+'\\')"><i class="ti ti-trash"></i></button></div>';
  const edit = '<div id="'+editId+'" style="display:none;background:var(--bg);border:1px solid var(--border);border-radius:8px;padding:10px;margin-bottom:8px">'
    +'<div class="grid2" style="grid-template-columns:1fr 1fr;gap:8px">'
    +'<div class="form-group" style="margin-bottom:6px"><label class="form-label">Punkt</label><input class="form-input mci-label" value="'+escapeHtml(i.label)+'"></div>'
    +'<div class="form-group" style="margin-bottom:6px"><label class="form-label">Typ</label><select class="form-input mci-type"><option value="checkbox"'+(i.item_type==='checkbox'?' selected':'')+'>Checkbox</option><option value="number"'+(i.item_type==='number'?' selected':'')+'>Messwert</option><option value="text"'+(i.item_type==='text'?' selected':'')+'>Text</option></select></div>'
    +'<div class="form-group" style="margin-bottom:6px"><label class="form-label">Intervall Tage</label><input type="number" min="1" class="form-input mci-interval" value="'+escapeHtml(i.interval_days)+'"></div>'
    +'<div class="form-group" style="margin-bottom:6px"><label class="form-label">Sortierung</label><input type="number" class="form-input mci-sort" value="'+escapeHtml(i.sort_order||100)+'"></div>'
    +'</div>'
    +'<label style="display:flex;align-items:center;gap:6px;font-size:11px;color:var(--muted);margin-bottom:8px"><input type="checkbox" class="mci-required" '+(i.required?'checked':'')+'> Pflichtpunkt</label>'
    +'<div style="display:flex;justify-content:flex-end;gap:6px">'
    +'<button type="button" class="btn" onclick="toggleMaintenanceChecklistItemEdit(\\''+escapeJs(i.id)+'\\')">Abbrechen</button>'
    +'<button type="button" class="btn btn-primary" onclick="updateMaintenanceChecklistItem(\\''+escapeJs(i.id)+'\\')">Speichern</button>'
    +'</div></div>';
  return view + edit;
}
function toggleMaintenanceChecklistItemEdit(itemID){
  const v = document.getElementById('mci-view-'+itemID);
  const e = document.getElementById('mci-edit-'+itemID);
  if(!v || !e) return;
  const showEdit = e.style.display === 'none' || !e.style.display;
  e.style.display = showEdit ? 'block' : 'none';
  v.style.display = showEdit ? 'none' : 'flex';
}
async function updateMaintenanceChecklistItem(itemID){
  const editBox = document.getElementById('mci-edit-'+itemID);
  if(!editBox) return;
  const payload = {
    label: editBox.querySelector('.mci-label').value,
    item_type: editBox.querySelector('.mci-type').value,
    interval_days: Number(editBox.querySelector('.mci-interval').value || 0),
    sort_order: Number(editBox.querySelector('.mci-sort').value || 100),
    required: editBox.querySelector('.mci-required').checked,
    description: ''
  };
  try{
    await maintenanceJSON('/api/v1/maintenance-checklists/items/'+encodeURIComponent(itemID), {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify(payload)});
    await loadMaintenanceChecklistTemplates();
  }catch(e){ alert('Fehler beim Speichern: '+e.message); }
}'''

c = c.replace(old_func, new_func, 1)

with open(path, "w", encoding="utf-8") as f:
    f.write(c)
print("OK: " + path + " gepatcht.")


