import re

path = "web/templates/maintenance_detail.gohtml"
with open(path, "r", encoding="utf-8") as f:
    content = f.read()

# ── 1) Alten "Abschließen"-Popup-Block ersetzen durch Ausführungsformular ──
old_complete = '''<!-- Abschließen -->
{{if .Task.CanComplete}}
<div id="complete-form" style="display:none" class="card">
  <div class="card-title">Auftrag durchführen und abschließen</div>
  <form onsubmit="return submitMaintenanceCompletion(this,'{{.Task.ID}}')">
    <div class="grid2">
      <div class="form-group"><label class="form-label">Dauer (Minuten)</label><input id="complete-duration" name="duration_min" type="number" class="form-input" placeholder="Standarddauer wird geladen" min="0"></div>
      <div class="form-group"><label class="form-label">Abschlussnotiz</label><input name="notes" class="form-input" placeholder="Was wurde gemacht?"></div>
    </div>
    <div id="due-checklist-box" style="margin:10px 0;font-size:12px;color:var(--muted)">Fällige Checklistenpunkte werden geladen...</div>
    <div class="card" style="background:var(--bg3);margin:10px 0;padding:12px">
      <div style="font-size:12px;font-weight:600;margin-bottom:8px">Nach Abschluss</div>
      <label style="display:flex;gap:8px;align-items:center;font-size:12px;color:var(--muted);margin-bottom:6px"><input type="checkbox" name="ask_inventory" checked> Nach verwendeten Ersatzteilen fragen</label>
      <label style="display:flex;gap:8px;align-items:center;font-size:12px;color:var(--muted)"><input type="checkbox" name="ask_time" checked> Nach Zeiteintrag fragen / Dauer übernehmen</label>
    </div>
    <div id="complete-result" style="font-size:12px;margin-bottom:8px"></div>
    {{template "icon-button-toolbar" (dict
      "Class" "end"
      "Label" "Auftrag abschließen"
      "Style" "width:100%;justify-content:flex-end"
      "Items" (list
        (dict "Action" "cancel" "ShowLabel" true "OnClick" "document.getElementById('complete-form').style.display='none'")
        (dict "Action" "done" "ShowLabel" true "Type" "submit" "Label" "Abschließen")
      )
    )}}
  </form>
</div>
{{end}}'''

new_complete = '''<!-- Ausführungsformular -->
{{if .Task.CanComplete}}
<div id="complete-form" class="card">
  <div class="card-title"><i class="ti ti-clipboard-check" style="color:var(--accent)"></i> Ausführungsformular</div>
  <form onsubmit="return submitMaintenanceCompletion(this,'{{.Task.ID}}')">

    {{if .ChecklistItems}}
    <div style="margin-bottom:16px">
      <div style="font-size:12px;font-weight:600;text-transform:uppercase;letter-spacing:.8px;color:var(--muted);margin-bottom:8px">Checkliste</div>
      {{range .ChecklistItems}}
      <div class="li" style="flex-direction:column;align-items:stretch;gap:6px;padding:10px 0">
        <div style="display:flex;align-items:center;gap:8px">
          <div style="font-size:13px;font-weight:500;flex:1">
            {{if .Required}}<span style="color:var(--red);font-size:10px">✱</span> {{end}}{{.Label}}
          </div>
          <span style="font-size:10px;color:var(--muted)">{{.Type}}</span>
        </div>
        {{if eq .Type "checkbox"}}
        <label style="display:flex;align-items:center;gap:8px;cursor:pointer;font-size:13px">
          <input type="checkbox" name="item_{{.ID}}" value="1" style="width:16px;height:16px">
          <span style="color:var(--muted)">Erledigt</span>
        </label>
        {{else if eq .Type "number"}}
        <div style="display:flex;align-items:center;gap:8px">
          <input type="number" name="item_{{.ID}}" class="form-input" style="max-width:160px" placeholder="Messwert eingeben" {{if .CompareValue}}step="0.01"{{end}}>
          {{if .CompareUnit}}<span style="font-size:12px;color:var(--muted)">{{.CompareUnit}}</span>{{end}}
        </div>
        {{else if eq .Type "image"}}
        <label style="cursor:pointer;display:inline-flex;align-items:center;gap:6px;background:var(--bg3);border:1px solid var(--border);border-radius:8px;padding:7px 12px;font-size:12px;color:var(--muted)">
          <i class="ti ti-camera"></i>Foto aufnehmen
          <input type="file" accept="image/*" capture="environment" name="item_{{.ID}}" style="display:none">
        </label>
        {{else}}
        <input type="text" name="item_{{.ID}}" class="form-input" placeholder="{{.Label}}">
        {{end}}
      </div>
      {{end}}
    </div>
    {{end}}

    <div id="due-checklist-box" style="margin-bottom:16px;font-size:12px;color:var(--muted)">Fällige Checklistenpunkte werden geladen...</div>

    <div style="margin-bottom:16px">
      <div style="font-size:12px;font-weight:600;text-transform:uppercase;letter-spacing:.8px;color:var(--muted);margin-bottom:8px">Durchgeführte Maßnahmen</div>
      <div id="maint-actions-list" style="margin-bottom:10px;font-size:12px;color:var(--muted)">Lade...</div>
      <div style="display:flex;gap:8px">
        <input id="ma-new-action" class="form-input" placeholder="z.B. Filter getauscht" style="flex:1" onkeydown="if(event.key==='Enter'){event.preventDefault();addMaintAction();}">
        <button type="button" class="btn" onclick="addMaintAction()"><i class="ti ti-plus"></i></button>
      </div>
    </div>

    <div style="margin-bottom:16px">
      <div style="font-size:12px;font-weight:600;text-transform:uppercase;letter-spacing:.8px;color:var(--muted);margin-bottom:8px">Ersatzteile</div>
      <div id="maint-parts-list" style="margin-bottom:10px;font-size:12px;color:var(--muted)">Lade...</div>
      <button type="button" class="btn" onclick="openMaintPartModal()"><i class="ti ti-package"></i> Ersatzteil vormerken</button>
      <label style="display:flex;gap:8px;align-items:center;font-size:12px;color:var(--muted);margin-top:10px">
        <input type="checkbox" name="no_parts_needed"> Keine Ersatzteile benötigt
      </label>
    </div>

    <div class="grid2" style="margin-bottom:16px">
      <div class="form-group"><label class="form-label">Dauer (Minuten)</label><input id="complete-duration" name="duration_min" type="number" class="form-input" placeholder="Standarddauer wird geladen" min="0"></div>
      <div class="form-group"><label class="form-label">Abschlussnotiz</label><input name="notes" class="form-input" placeholder="Was wurde gemacht?"></div>
    </div>
    <div class="card" style="background:var(--bg3);margin:10px 0;padding:12px">
      <div style="font-size:12px;font-weight:600;margin-bottom:8px">Nach Abschluss</div>
      <label style="display:flex;gap:8px;align-items:center;font-size:12px;color:var(--muted);margin-bottom:6px"><input type="checkbox" name="ask_inventory" checked> Nach verwendeten Ersatzteilen fragen</label>
      <label style="display:flex;gap:8px;align-items:center;font-size:12px;color:var(--muted)"><input type="checkbox" name="ask_time" checked> Nach Zeiteintrag fragen / Dauer übernehmen</label>
    </div>
    <div id="complete-result" style="font-size:12px;margin-bottom:8px"></div>
    {{template "icon-button-toolbar" (dict
      "Class" "end"
      "Label" "Auftrag abschließen"
      "Style" "width:100%;justify-content:flex-end"
      "Items" (list
        (dict "Action" "done" "ShowLabel" true "Type" "submit" "Label" "Abschließen")
      )
    )}}
  </form>
</div>

<!-- Ersatzteil-vormerken Popup -->
<div id="maint-part-modal" style="display:none;position:fixed;inset:0;background:rgba(0,0,0,.65);z-index:6000;align-items:center;justify-content:center">
  <div style="background:var(--bg2);border:1px solid var(--border);border-radius:14px;padding:22px;width:100%;max-width:420px;margin:20px">
    <div style="font-size:15px;font-weight:600;margin-bottom:14px">Ersatzteil vormerken</div>
    <div class="form-group">
      <label class="form-label">Ersatzteil</label>
      <input id="ma-part-search" class="form-input" placeholder="Suchen..." oninput="filterMaintParts()" autocomplete="off">
      <div id="ma-part-results" style="max-height:160px;overflow-y:auto;border:1px solid var(--border);border-radius:8px;margin-top:4px;display:none"></div>
      <input type="hidden" id="ma-part-id">
      <div id="ma-part-selected" style="font-size:12px;color:var(--muted);margin-top:4px"></div>
    </div>
    <div class="form-group">
      <label class="form-label">Lagerort</label>
      <select id="ma-part-location" class="form-input"><option value="">Lade Lagerorte...</option></select>
    </div>
    <div class="form-group">
      <label class="form-label">Menge</label>
      <input id="ma-part-qty" type="number" step="0.001" class="form-input" value="1">
    </div>
    <div id="ma-part-error" style="display:none;color:var(--red);font-size:12px;margin-bottom:8px"></div>
    <div style="display:flex;justify-content:flex-end;gap:8px;margin-top:12px">
      <button type="button" class="btn" onclick="closeMaintPartModal()">Abbrechen</button>
      <button type="button" class="btn btn-primary" onclick="bookMaintPart()">Vormerken</button>
    </div>
  </div>
</div>
{{end}}'''

if old_complete not in content:
    raise SystemExit("FEHLER: alter Abschließen-Block nicht gefunden (Block 1)")
content = content.replace(old_complete, new_complete, 1)

# ── 2) Alte eigenständige Checkliste-Karte entfernen (jetzt im Formular) ──
old_checklist = '''    <!-- Checkliste -->
    {{if .ChecklistItems}}
    <div class="card">
      <div class="card-title"><i class="ti ti-checklist" style="color:var(--accent)"></i> Checkliste</div>
      <form method="POST" action="/maintenance/tasks/{{.Task.ID}}/checklist">
        {{range .ChecklistItems}}
        <div class="li" style="flex-direction:column;align-items:stretch;gap:6px;padding:10px 0">
          <div style="display:flex;align-items:center;gap:8px">
            <div style="font-size:13px;font-weight:500;flex:1">
              {{if .Required}}<span style="color:var(--red);font-size:10px">✱</span> {{end}}{{.Label}}
            </div>
            <span style="font-size:10px;color:var(--muted)">{{.Type}}</span>
          </div>
          {{if eq .Type "checkbox"}}
          <label style="display:flex;align-items:center;gap:8px;cursor:pointer;font-size:13px">
            <input type="checkbox" name="item_{{.ID}}" value="1" style="width:16px;height:16px">
            <span style="color:var(--muted)">Erledigt</span>
          </label>
          {{else if eq .Type "number"}}
          <div style="display:flex;align-items:center;gap:8px">
            <input type="number" name="item_{{.ID}}" class="form-input" style="max-width:160px" placeholder="Messwert eingeben" {{if .CompareValue}}step="0.01"{{end}}>
            {{if .CompareUnit}}<span style="font-size:12px;color:var(--muted)">{{.CompareUnit}}</span>{{end}}
          </div>
          {{else if eq .Type "image"}}
          <label style="cursor:pointer;display:inline-flex;align-items:center;gap:6px;background:var(--bg3);border:1px solid var(--border);border-radius:8px;padding:7px 12px;font-size:12px;color:var(--muted)">
            <i class="ti ti-camera"></i>Foto aufnehmen
            <input type="file" accept="image/*" capture="environment" name="item_{{.ID}}" style="display:none">
          </label>
          {{else}}
          <input type="text" name="item_{{.ID}}" class="form-input" placeholder="{{.Label}}">
          {{end}}
        </div>
        {{end}}
        {{template "icon-button-toolbar" (dict
          "Class" "end"
          "Label" "Checkliste"
          "Style" "width:100%;justify-content:flex-end;padding-top:10px;border-top:1px solid var(--border)"
          "Items" (list
            (dict "Action" "save" "ShowLabel" true "Type" "submit" "Label" "Checkliste speichern")
          )
        )}}
      </form>
    </div>
    {{end}}
'''

if old_checklist not in content:
    raise SystemExit("FEHLER: alte Checkliste-Karte nicht gefunden (Block 2)")
content = content.replace(old_checklist, "", 1)

# ── 3) Neue JS-Funktionen vor </script> einfügen ──
old_script_end = '''function escapeHtml(s){return String(s||'').replace(/[&<>'"]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'}[c]));}
</script>
{{end}}'''

new_js = '''function escapeHtml(s){return String(s||'').replace(/[&<>'"]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'}[c]));}

// ── Durchgeführte Maßnahmen + Ersatzteile (Ausführungsformular) ─────────
var MAINT_TASK_ID = '{{.Task.ID}}';
var maPartsCache = null;
function escMa(s){return String(s||'').replace(/[&<>'"]/g,function(c){return ({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'}[c]);});}

async function loadMaintActions(){
  var box = document.getElementById('maint-actions-list');
  if(!box) return;
  try{
    var res = await fetch('/api/v1/maintenance/tasks/'+MAINT_TASK_ID+'/actions', {credentials:'same-origin'});
    var json = await res.json();
    var actions = json.data || json || [];
    box.innerHTML = actions.length ? actions.map(function(a){
      var t = new Date(a.created_at);
      var timeStr = t.toLocaleString('de-DE', {day:'2-digit',month:'2-digit',hour:'2-digit',minute:'2-digit'});
      return '<div class="li"><div class="li-text"><div class="li-title" style="font-size:13px">'+escMa(a.description)+'</div><div class="li-sub">'+timeStr+' · '+escMa(a.created_by_name)+'</div></div></div>';
    }).join('') : '<div style="padding:4px 0">Noch keine Maßnahmen erfasst</div>';
  }catch(e){ box.innerHTML = '<span style="color:var(--red)">Fehler beim Laden</span>'; }
}
async function addMaintAction(){
  var input = document.getElementById('ma-new-action');
  var description = input.value.trim();
  if(!description) return;
  try{
    var res = await fetch('/api/v1/maintenance/tasks/'+MAINT_TASK_ID+'/actions', {
      method:'POST', credentials:'same-origin', headers:{'Content-Type':'application/json'},
      body: JSON.stringify({description:description})
    });
    var json = await res.json();
    if(!res.ok) throw new Error(json.error||'Fehler');
    input.value='';
    loadMaintActions();
  }catch(e){ alert('Fehler: '+e.message); }
}

async function loadMaintPartsList(){
  var box = document.getElementById('maint-parts-list');
  if(!box) return;
  try{
    var res = await fetch('/api/v1/maintenance/tasks/'+MAINT_TASK_ID+'/pending-parts', {credentials:'same-origin'});
    var json = await res.json();
    var pending = json.data || json || [];
    box.innerHTML = pending.length ? pending.map(function(p){
      return '<div class="li"><div class="li-text"><div class="li-title" style="font-size:13px">'+escMa(p.part_name)+' <span style="color:var(--muted)">('+escMa(p.part_number)+')</span></div><div class="li-sub">'+p.qty+' Stk · '+escMa(p.storage_name)+'</div></div><button type="button" class="btn" style="font-size:11px;color:var(--red)" onclick="removeMaintPendingPart(THISID)"><i class="ti ti-x"></i></button></div>'.replace('THISID', "'"+p.id+"'");
    }).join('') : '<div style="padding:4px 0">Noch keine Ersatzteile vorgemerkt</div>';
  }catch(e){ box.innerHTML = '<span style="color:var(--red)">Fehler beim Laden</span>'; }
}
async function removeMaintPendingPart(id){
  try{
    var res = await fetch('/api/v1/maintenance/tasks/'+MAINT_TASK_ID+'/pending-parts/'+id, {method:'DELETE', credentials:'same-origin'});
    var json = await res.json();
    if(!res.ok || json.success===false) throw new Error(json.error || ('HTTP '+res.status));
    loadMaintPartsList();
  }catch(e){ alert('Fehler beim Entfernen: '+e.message); }
}

async function loadMaintPartsCache(){
  if(maPartsCache) return maPartsCache;
  var res = await fetch('/api/v1/inventory/', {credentials:'same-origin'});
  var json = await res.json();
  maPartsCache = json.data || json || [];
  return maPartsCache;
}
async function loadMaintStorageOptions(selectEl){
  try{
    var res = await fetch('/api/v1/storage/', {credentials:'same-origin'});
    var json = await res.json();
    var tree = json.data || json;
    var options = [];
    function walk(nodes, prefix){
      (nodes||[]).forEach(function(n){
        var label = prefix ? prefix+' > '+n.name : n.name;
        options.push({id:n.id, label:label});
        if(n.children && n.children.length) walk(n.children, label);
      });
    }
    walk(Array.isArray(tree) ? tree : (tree.children||[]), '');
    selectEl.innerHTML = '<option value="">Lagerort wählen...</option>' + options.map(function(o){return '<option value="'+o.id+'">'+escMa(o.label)+'</option>';}).join('');
  }catch(e){
    selectEl.innerHTML = '<option value="">Fehler beim Laden</option>';
  }
}
function openMaintPartModal(){
  document.getElementById('maint-part-modal').style.display='flex';
  document.getElementById('ma-part-search').value='';
  document.getElementById('ma-part-id').value='';
  document.getElementById('ma-part-selected').textContent='';
  document.getElementById('ma-part-results').style.display='none';
  document.getElementById('ma-part-error').style.display='none';
  document.getElementById('ma-part-qty').value='1';
  loadMaintStorageOptions(document.getElementById('ma-part-location'));
}
function closeMaintPartModal(){ document.getElementById('maint-part-modal').style.display='none'; }
async function filterMaintParts(){
  var q = document.getElementById('ma-part-search').value.trim().toLowerCase();
  var box = document.getElementById('ma-part-results');
  if(!q){ box.style.display='none'; return; }
  var parts = await loadMaintPartsCache();
  var matches = parts.filter(function(p){
    return (p.name||'').toLowerCase().indexOf(q)!==-1 || (p.part_number||'').toLowerCase().indexOf(q)!==-1;
  }).slice(0,20);
  box.innerHTML = matches.length ? matches.map(function(p){
    return '<div style="padding:6px 8px;cursor:pointer;font-size:12px;border-bottom:1px solid var(--border)" onclick="selectMaintPart(\\''+p.id+'\\',\\''+escMa(p.name).replace(/'/g,"\\\\'")+'\\')">'+escMa(p.name)+' <span style="color:var(--muted)">('+escMa(p.part_number)+')</span></div>';
  }).join('') : '<div style="padding:6px 8px;font-size:12px;color:var(--muted)">Keine Treffer</div>';
  box.style.display='block';
}
function selectMaintPart(id,label){
  document.getElementById('ma-part-id').value=id;
  document.getElementById('ma-part-selected').textContent='Ausgewählt: '+label;
  document.getElementById('ma-part-results').style.display='none';
  document.getElementById('ma-part-search').value='';
}
async function bookMaintPart(){
  var partId = document.getElementById('ma-part-id').value;
  var locationId = document.getElementById('ma-part-location').value;
  var qty = parseFloat(document.getElementById('ma-part-qty').value);
  var errEl = document.getElementById('ma-part-error');
  errEl.style.display='none';
  if(!partId){ errEl.textContent='Bitte ein Ersatzteil auswählen'; errEl.style.display='block'; return; }
  if(!locationId){ errEl.textContent='Bitte einen Lagerort auswählen'; errEl.style.display='block'; return; }
  if(!qty || qty<=0){ errEl.textContent='Bitte eine gültige Menge angeben'; errEl.style.display='block'; return; }
  try{
    var res = await fetch('/api/v1/maintenance/tasks/'+MAINT_TASK_ID+'/pending-parts', {
      method:'POST', credentials:'same-origin', headers:{'Content-Type':'application/json'},
      body: JSON.stringify({part_id:partId, storage_node_id:locationId, qty:qty})
    });
    var json = await res.json();
    if(!res.ok || json.success===false) throw new Error(json.error || ('HTTP '+res.status));
    closeMaintPartModal();
    loadMaintPartsList();
  }catch(e){ errEl.textContent='Fehler: '+e.message; errEl.style.display='block'; }
}

if({{if .Task.CanComplete}}true{{else}}false{{end}}){
  if(document.readyState !== 'loading'){ loadMaintActions(); loadMaintPartsList(); openCompleteForm(MAINT_TASK_ID); }
  else document.addEventListener('DOMContentLoaded', function(){ loadMaintActions(); loadMaintPartsList(); openCompleteForm(MAINT_TASK_ID); });
}
</script>
{{end}}'''

if old_script_end not in content:
    raise SystemExit("FEHLER: Script-Ende nicht gefunden (Block 3)")
content = content.replace(old_script_end, new_js, 1)

with open(path, "w", encoding="utf-8") as f:
    f.write(content)

print("OK: " + path + " gepatcht (3 Stellen ersetzt).")
