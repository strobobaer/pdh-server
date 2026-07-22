cat > apply_maintenance_checklist_fixes.py <<'PYEOF'
import re

# ── 1) checklist_templates.go: Update-Methode ergänzen ──────────────────
path1 = "internal/modules/maintenance/checklist_templates.go"
with open(path1, encoding="utf-8") as f:
    c1 = f.read()

anchor1 = '''func (r *Repository) DeleteChecklistTemplateItem(ctx context.Context, itemID string) error {
        if err := r.ensureChecklistTemplateTables(ctx); err != nil { return err }
        _, err := r.db.Exec(ctx, `UPDATE maintenance_checklist_template_items SET active=false WHERE id=$1`, itemID)
        return err
}'''

addition1 = anchor1 + '''

func (r *Repository) UpdateChecklistTemplateItem(ctx context.Context, itemID string, item *ChecklistTemplateItem) error {
        if err := r.ensureChecklistTemplateTables(ctx); err != nil { return err }
        if item.ItemType == "" { item.ItemType = "checkbox" }
        if item.SortOrder == 0 { item.SortOrder = 100 }
        _, err := r.db.Exec(ctx, `UPDATE maintenance_checklist_template_items SET label=$1, description=$2, item_type=$3, required=$4, interval_days=$5, sort_order=$6 WHERE id=$7`,
                item.Label, item.Description, item.ItemType, item.Required, item.IntervalDays, item.SortOrder, itemID)
        return err
}'''

if anchor1 not in c1:
    raise SystemExit("FEHLER: Anker 1 nicht gefunden (checklist_templates.go)")
c1 = c1.replace(anchor1, addition1, 1)
with open(path1, "w", encoding="utf-8") as f:
    f.write(c1)
print("OK: " + path1 + " gepatcht.")

# ── 2) checklist_api.go: Service + Handler + Route ergänzen ─────────────
path2 = "internal/modules/maintenance/checklist_api.go"
with open(path2, encoding="utf-8") as f:
    c2 = f.read()

anchor2a = '''func (s *Service) DeleteChecklistTemplateItemForAPI(r *http.Request, itemID string) error {
        return s.repo.DeleteChecklistTemplateItem(r.Context(), itemID)
}'''
addition2a = anchor2a + '''

func (s *Service) UpdateChecklistTemplateItemForAPI(r *http.Request, itemID string, in createChecklistTemplateItemInput) (*ChecklistTemplateItem, error) {
        item := &ChecklistTemplateItem{
                Label: strings.TrimSpace(in.Label), Description: strings.TrimSpace(in.Description),
                ItemType: in.ItemType, Required: in.Required, IntervalDays: in.IntervalDays, SortOrder: in.SortOrder,
        }
        if item.ItemType == "" { item.ItemType = "checkbox" }
        if item.IntervalDays <= 0 { item.IntervalDays = 1 }
        if item.SortOrder == 0 { item.SortOrder = 100 }
        if err := s.repo.UpdateChecklistTemplateItem(r.Context(), itemID, item); err != nil { return nil, err }
        item.ID = itemID
        return item, nil
}'''
if anchor2a not in c2:
    raise SystemExit("FEHLER: Anker 2a nicht gefunden (checklist_api.go, Service)")
c2 = c2.replace(anchor2a, addition2a, 1)

anchor2b = '        r.Delete("/items/{itemID}", h.DeleteChecklistTemplateItem)'
addition2b = anchor2b + '\n        r.Post("/items/{itemID}", h.UpdateChecklistTemplateItem)'
if anchor2b not in c2:
    raise SystemExit("FEHLER: Anker 2b nicht gefunden (checklist_api.go, Route)")
c2 = c2.replace(anchor2b, addition2b, 1)

anchor2c = '''func (h *Handler) DeleteChecklistTemplateItem(w http.ResponseWriter, r *http.Request) {
        if err := h.svc.DeleteChecklistTemplateItemForAPI(r, chi.URLParam(r, "itemID")); err != nil { response.Error(w, 500, err.Error()); return }
        response.JSON(w, 200, map[string]string{"status":"gelöscht"})
}'''
addition2c = anchor2c + '''

func (h *Handler) UpdateChecklistTemplateItem(w http.ResponseWriter, r *http.Request) {
        var in createChecklistTemplateItemInput
        if err := json.NewDecoder(r.Body).Decode(&in); err != nil { response.Error(w, 400, "ungültige eingabe"); return }
        if strings.TrimSpace(in.Label) == "" { response.Error(w, 400, "label ist pflicht"); return }
        if in.IntervalDays <= 0 { response.Error(w, 400, "intervall ist pflicht"); return }
        item, err := h.svc.UpdateChecklistTemplateItemForAPI(r, chi.URLParam(r, "itemID"), in)
        if err != nil { response.Error(w, 500, err.Error()); return }
        response.JSON(w, 200, item)
}'''
if anchor2c not in c2:
    raise SystemExit("FEHLER: Anker 2c nicht gefunden (checklist_api.go, Handler)")
c2 = c2.replace(anchor2c, addition2c, 1)

with open(path2, "w", encoding="utf-8") as f:
    f.write(c2)
print("OK: " + path2 + " gepatcht.")

# ── 3) maintenance.gohtml: PUT-Bug fixen + Bearbeiten-Button für Punkte ──
path3 = "web/templates/maintenance.gohtml"
with open(path3, encoding="utf-8") as f:
    c3 = f.read()

old_fetch = "const res=await fetch('/maintenance/plans/'+encodeURIComponent(id)+'/edit-web',{method:'PUT',headers:{'HX-Request':'true'},body:formData,credentials:'same-origin'});"
new_fetch = "const res=await fetch('/maintenance/plans/'+encodeURIComponent(id)+'/edit-web',{method:'POST',headers:{'HX-Request':'true'},body:formData,credentials:'same-origin'});"
if old_fetch not in c3:
    raise SystemExit("FEHLER: PUT-Bug-Stelle nicht gefunden (maintenance.gohtml)")
c3 = c3.replace(old_fetch, new_fetch, 1)

old_render = '''async function renderMaintenanceChecklistTemplateList(){
  const box = document.getElementById('maintenance-checklist-template-list');
  if(!box) return;
  if(!maintenanceChecklistTemplates.length){ box.innerHTML='Noch keine Vorlagen vorhanden'; return; }
  const blocks = [];
  for(const t of maintenanceChecklistTemplates){
    let items = [];
    try{ items = asArray(await maintenanceJSON('/api/v1/maintenance-checklists/templates/'+encodeURIComponent(t.id)+'/items')); }catch(e){ items = []; }
    const itemHtml = items.length ? items.map(i => '<div class="li"><div class="li-text"><div class="li-title" style="font-size:12px">'+escapeHtml(i.label)+'</div><div class="li-sub">'+escapeHtml(i.item_type)+' · Intervall '+escapeHtml(i.interval_days)+' Tage'+(i.required?' · Pflicht':'')+'</div></div><button type="button" class="btn btn-danger" onclick="deleteMaintenanceChecklistItem(\\''+escapeJs(i.id)+'\\')"><i class="ti ti-trash"></i></button></div>').join('') : '<div style="font-size:11px;color:var(--muted)">Keine Punkte</div>';
    blocks.push('<div style="border:1px solid var(--border);border-radius:8px;padding:10px;margin-bottom:8px;background:var(--bg3)"><div style="display:flex;align-items:center;gap:8px;margin-bottom:4px"><div style="font-weight:600;color:var(--text);flex:1">'+escapeHtml(t.name)+'</div><button type="button" class="btn btn-danger" onclick="deleteMaintenanceChecklistTemplate(\\''+escapeJs(t.id)+'\\')"><i class="ti ti-trash"></i></button></div><div style="font-size:11px;color:var(--muted);margin-bottom:6px">'+escapeHtml(t.description||'')+'</div>'+itemHtml+'</div>');
  }
  box.innerHTML = blocks.join('');
}'''

new_render = '''async function renderMaintenanceChecklistTemplateList(){
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
  const view = '<div id="'+viewId+'" class="li"><div class="li-text"><div class="li-title" style="font-size:12px">'+escapeHtml(i.label)+'</div><div class="li-sub">'+escapeHtml(i.item_type)+' · Intervall '+escapeHtml(i.interval_days)+' Tage'+(i.required?' · Pflicht':'')+'</div></div><button type="button" class="btn" onclick="toggleMaintenanceChecklistItemEdit(\\''+escapeJs(i.id)+'\\')"><i class="ti ti-edit"></i></button><button type="button" class="btn btn-danger" onclick="deleteMaintenanceChecklistItem(\\''+escapeJs(i.id)+'\\')"><i class="ti ti-trash"></i></button></div>';
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

if old_render not in c3:
    raise SystemExit("FEHLER: render-Block nicht gefunden (maintenance.gohtml)")
c3 = c3.replace(old_render, new_render, 1)

with open(path3, "w", encoding="utf-8") as f:
    f.write(c3)
print("OK: " + path3 + " gepatcht.")
PYEOF
python3 apply_maintenance_checklist_fixes.py
