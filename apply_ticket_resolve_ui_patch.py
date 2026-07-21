#!/usr/bin/env python3
"""
Patcht web/templates/ticket_detail.gohtml: fügt "Ticket lösen"-Formular
(Maßnahme + Grundursache + "keine Teile benötigt"), Maßnahmen-Verlauf und
Ersatzteil-Merkliste hinzu - analog zur Störungsseite. Der gelöst-Status wird
per JS aus der JSON-API ermittelt (kein Zugriff auf unbekannte TicketView-
Felder nötig).

Aufruf:
    python3 apply_ticket_resolve_ui_patch.py web/templates/ticket_detail.gohtml
"""
import sys

CARDS_OLD = '''      <form hx-post="/tickets/{{.Ticket.ID}}/comment" hx-target="#comments-list" hx-swap="beforeend" style="margin-top:12px;display:flex;gap:8px">
        <input name="text" class="form-input" placeholder="Kommentar hinzufügen..." style="flex:1;font-size:12px" required>
        {{template "icon-button" (dict "Action" "send" "Type" "submit")}}
      </form>
    </div>

    <!-- Anhänge -->'''

CARDS_NEW = '''      <form hx-post="/tickets/{{.Ticket.ID}}/comment" hx-target="#comments-list" hx-swap="beforeend" style="margin-top:12px;display:flex;gap:8px">
        <input name="text" class="form-input" placeholder="Kommentar hinzufügen..." style="flex:1;font-size:12px" required>
        {{template "icon-button" (dict "Action" "send" "Type" "submit")}}
      </form>
    </div>

    <!-- Ticket lösen -->
    <div id="ticket-resolve-card" class="card" style="display:none">
      <div class="card-title">Ticket lösen</div>
      <form hx-post="/tickets/{{.Ticket.ID}}/resolve-web" hx-target="#ticket-resolve-result" hx-swap="innerHTML" hx-on::after-request="if(!event.detail.xhr.responseText.includes('var(--red)')) setTimeout(function(){location.reload();}, 1200)">
        <div class="form-group">
          <label class="form-label">Lösung / Maßnahme</label>
          <textarea name="resolution" class="form-input" rows="3" placeholder="Was wurde gemacht?" required style="resize:vertical"></textarea>
        </div>
        <div class="form-group">
          <label class="form-label">Grundursache</label>
          <input name="root_cause" class="form-input" placeholder="Warum ist es passiert?">
        </div>
        <div class="form-group">
          <label style="display:flex;align-items:center;gap:6px;font-size:12px;cursor:pointer">
            <input type="checkbox" name="no_parts_needed"> Keine Ersatzteile benötigt
          </label>
        </div>
        <div id="ticket-resolve-result" style="font-size:12px;margin-bottom:8px"></div>
        {{template "icon-button-toolbar" (dict
          "Class" "end"
          "Label" "Ticket lösen"
          "Style" "width:100%;justify-content:flex-end"
          "Items" (list
            (dict "Action" "save" "ShowLabel" true "Type" "submit" "Label" "Ticket lösen")
          )
        )}}
      </form>
    </div>

    <!-- Durchgeführte Maßnahmen -->
    <div class="card">
      <div class="card-title">Durchgeführte Maßnahmen</div>
      <div id="ticket-actions-list" style="margin-bottom:10px;font-size:12px;color:var(--muted)">Lade...</div>
      <div id="ticket-actions-add" style="display:flex;gap:8px">
        <input id="ta-new-action" class="form-input" placeholder="z.B. Ersatzteil bestellt" style="flex:1" onkeydown="if(event.key==='Enter'){event.preventDefault();addTicketAction();}">
        <button type="button" class="btn" onclick="addTicketAction()"><i class="ti ti-plus"></i></button>
      </div>
    </div>

    <!-- Ersatzteile -->
    <div class="card">
      <div class="card-title" id="ticket-parts-title">Ersatzteile für die Reparatur</div>
      <p id="ticket-parts-hint" style="font-size:11px;color:var(--muted);margin-bottom:8px">Wird erst beim Lösen des Tickets tatsächlich aus dem Lager gebucht.</p>
      <div id="ticket-parts-usage-list" style="margin-bottom:10px;font-size:12px;color:var(--muted)">Lade...</div>
      <button type="button" id="ticket-parts-add-btn" class="btn" onclick="openTicketPartModal()"><i class="ti ti-package"></i> Ersatzteil vormerken</button>
    </div>

    <!-- Ersatzteil-vormerken Popup -->
    <div id="ticket-part-modal" style="display:none;position:fixed;inset:0;background:rgba(0,0,0,.65);z-index:6000;align-items:center;justify-content:center">
      <div style="background:var(--bg2);border:1px solid var(--border);border-radius:14px;padding:22px;width:100%;max-width:420px;margin:20px">
        <div style="font-size:15px;font-weight:600;margin-bottom:14px">Ersatzteil vormerken</div>
        <div class="form-group">
          <label class="form-label">Ersatzteil</label>
          <input id="tp-part-search" class="form-input" placeholder="Suchen..." oninput="filterTicketParts()" autocomplete="off">
          <div id="tp-part-results" style="max-height:160px;overflow-y:auto;border:1px solid var(--border);border-radius:8px;margin-top:4px;display:none"></div>
          <input type="hidden" id="tp-part-id">
          <div id="tp-part-selected" style="font-size:12px;color:var(--muted);margin-top:4px"></div>
        </div>
        <div class="form-group">
          <label class="form-label">Lagerort</label>
          <select id="tp-part-location" class="form-input"><option value="">Lade Lagerorte...</option></select>
        </div>
        <div class="form-group">
          <label class="form-label">Menge</label>
          <input id="tp-part-qty" type="number" step="0.001" class="form-input" value="1">
        </div>
        <div id="tp-part-error" style="display:none;color:var(--red);font-size:12px;margin-bottom:8px"></div>
        <div style="display:flex;justify-content:flex-end;gap:8px;margin-top:12px">
          <button type="button" class="btn" onclick="closeTicketPartModal()">Abbrechen</button>
          <button type="button" class="btn btn-primary" onclick="bookTicketPart()">Vormerken</button>
        </div>
      </div>
    </div>

    <!-- Anhänge -->'''

SCRIPTS_OLD = '''function toggleMenu(id) {
  const m = document.getElementById(id);
  m.style.display = m.style.display === 'none' ? 'block' : 'none';
}
function closeMenu(id) { document.getElementById(id).style.display='none'; }
document.addEventListener('click', e => {
  if(!e.target.closest('[onclick*="toggleMenu"]') && !e.target.closest('[id$="-menu"]'))
    document.querySelectorAll('[id$="-menu"]').forEach(m=>m.style.display='none');
});
</script>
{{end}}'''

SCRIPTS_NEW = '''function toggleMenu(id) {
  const m = document.getElementById(id);
  m.style.display = m.style.display === 'none' ? 'block' : 'none';
}
function closeMenu(id) { document.getElementById(id).style.display='none'; }
document.addEventListener('click', e => {
  if(!e.target.closest('[onclick*="toggleMenu"]') && !e.target.closest('[id$="-menu"]'))
    document.querySelectorAll('[id$="-menu"]').forEach(m=>m.style.display='none');
});

// ── Ticket lösen: Maßnahmen-Verlauf + Ersatzteil-Merkliste ──────
const TICKET_ID = '{{.Ticket.ID}}';
let TICKET_RESOLVED = false;
let tPartsCache = null;

function escTk(s){return String(s||'').replace(/[&<>'"]/g,function(c){return ({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'})[c];});}

async function initTicketResolveUI(){
  try{
    const res = await fetch('/api/v1/tickets/'+TICKET_ID, {credentials:'same-origin'});
    const json = await res.json();
    const t = json.data || json;
    TICKET_RESOLVED = (t.status === 'resolved' || t.status === 'closed');
  }catch(e){ TICKET_RESOLVED = false; }

  const resolveCard = document.getElementById('ticket-resolve-card');
  const partsHint = document.getElementById('ticket-parts-hint');
  const partsAddBtn = document.getElementById('ticket-parts-add-btn');
  const partsTitle = document.getElementById('ticket-parts-title');
  const actionsAdd = document.getElementById('ticket-actions-add');

  if(TICKET_RESOLVED){
    resolveCard.style.display = 'none';
    partsHint.style.display = 'none';
    partsAddBtn.style.display = 'none';
    partsTitle.textContent = 'Verwendete Ersatzteile';
    actionsAdd.style.display = 'none';
  } else {
    resolveCard.style.display = 'block';
  }

  loadTicketActions();
  loadTicketPartsUsage();
}

async function loadTicketActions(){
  const box = document.getElementById('ticket-actions-list');
  try{
    const res = await fetch('/api/v1/tickets/'+TICKET_ID+'/actions', {credentials:'same-origin'});
    const json = await res.json();
    const actions = json.data || json || [];
    box.innerHTML = actions.length ? actions.map(function(a){
      const dt = new Date(a.created_at);
      const timeStr = dt.toLocaleString('de-DE', {day:'2-digit',month:'2-digit',hour:'2-digit',minute:'2-digit'});
      return '<div class="li"><div class="li-text"><div class="li-title" style="font-size:13px">'+escTk(a.description)+'</div><div class="li-sub">'+timeStr+' · '+escTk(a.created_by_name)+'</div></div></div>';
    }).join('') : '<div style="padding:4px 0">Noch keine Maßnahmen erfasst</div>';
  }catch(e){ box.innerHTML = '<span style="color:var(--red)">Fehler beim Laden</span>'; }
}
async function addTicketAction(){
  const input = document.getElementById('ta-new-action');
  const description = input.value.trim();
  if(!description) return;
  try{
    const res = await fetch('/api/v1/tickets/'+TICKET_ID+'/actions', {
      method:'POST', credentials:'same-origin', headers:{'Content-Type':'application/json'},
      body: JSON.stringify({description:description})
    });
    const json = await res.json();
    if(!res.ok) throw new Error(json.error||'Fehler');
    input.value='';
    loadTicketActions();
  }catch(e){ alert('Fehler: '+e.message); }
}

async function loadTicketPartsUsage(){
  const box = document.getElementById('ticket-parts-usage-list');
  try{
    if(TICKET_RESOLVED){
      const res = await fetch('/api/v1/tickets/'+TICKET_ID+'/parts-usage', {credentials:'same-origin'});
      const json = await res.json();
      const movements = json.data || json || [];
      box.innerHTML = movements.length ? movements.map(function(m){
        return '<div class="li"><div class="li-text"><div class="li-title" style="font-size:13px">'+escTk(m.part_name)+' <span style="color:var(--muted)">('+escTk(m.part_number)+')</span></div><div class="li-sub">'+m.qty+' Stk · '+escTk(m.created_by_name)+'</div></div></div>';
      }).join('') : '<div style="padding:4px 0">Keine Ersatzteile verwendet</div>';
      return;
    }
    const res = await fetch('/api/v1/tickets/'+TICKET_ID+'/pending-parts', {credentials:'same-origin'});
    const json = await res.json();
    const pending = json.data || json || [];
    box.innerHTML = pending.length ? pending.map(function(p){
      const row = '<div class="li"><div class="li-text"><div class="li-title" style="font-size:13px">'+escTk(p.part_name)+' <span style="color:var(--muted)">('+escTk(p.part_number)+')</span></div><div class="li-sub">'+p.qty+' Stk · '+escTk(p.storage_name)+'</div></div><button type="button" class="btn" style="font-size:11px;color:var(--red)" onclick="removeTicketPendingPart(PID)"><i class="ti ti-x"></i></button></div>';
      return row.replace('PID', "'"+p.id+"'");
    }).join('') : '<div style="padding:4px 0">Noch keine Ersatzteile vorgemerkt</div>';
  }catch(e){ box.innerHTML = '<span style="color:var(--red)">Fehler beim Laden</span>'; }
}

async function removeTicketPendingPart(id){
  try{
    const res = await fetch('/api/v1/tickets/'+TICKET_ID+'/pending-parts/'+id, {method:'DELETE', credentials:'same-origin'});
    const json = await res.json();
    if(!res.ok || json.success===false) throw new Error(json.error || ('HTTP '+res.status));
    loadTicketPartsUsage();
  }catch(e){ alert('Fehler beim Entfernen: '+e.message); }
}

async function loadTicketPartsCache(){
  if(tPartsCache) return tPartsCache;
  const res = await fetch('/api/v1/inventory/', {credentials:'same-origin'});
  const json = await res.json();
  tPartsCache = json.data || json || [];
  return tPartsCache;
}

async function loadTicketStorageFlatOptions(selectEl){
  try{
    const res = await fetch('/api/v1/storage/', {credentials:'same-origin'});
    const json = await res.json();
    const tree = json.data || json;
    const options = [];
    function walk(nodes, prefix){
      (nodes||[]).forEach(function(n){
        const label = prefix ? prefix+' > '+n.name : n.name;
        options.push({id:n.id, label:label});
        if(n.children && n.children.length) walk(n.children, label);
      });
    }
    walk(Array.isArray(tree) ? tree : (tree.children||[]), '');
    selectEl.innerHTML = '<option value="">Lagerort wählen...</option>' + options.map(function(o){return '<option value="'+o.id+'">'+escTk(o.label)+'</option>';}).join('');
  }catch(e){
    selectEl.innerHTML = '<option value="">Fehler beim Laden</option>';
  }
}

function openTicketPartModal(){
  document.getElementById('ticket-part-modal').style.display='flex';
  document.getElementById('tp-part-search').value='';
  document.getElementById('tp-part-id').value='';
  document.getElementById('tp-part-selected').textContent='';
  document.getElementById('tp-part-results').style.display='none';
  document.getElementById('tp-part-error').style.display='none';
  document.getElementById('tp-part-qty').value='1';
  loadTicketStorageFlatOptions(document.getElementById('tp-part-location'));
}
function closeTicketPartModal(){ document.getElementById('ticket-part-modal').style.display='none'; }

async function filterTicketParts(){
  const q = document.getElementById('tp-part-search').value.trim().toLowerCase();
  const box = document.getElementById('tp-part-results');
  if(!q){ box.style.display='none'; return; }
  const parts = await loadTicketPartsCache();
  const matches = parts.filter(function(p){
    return (p.name||'').toLowerCase().includes(q) || (p.part_number||'').toLowerCase().includes(q);
  }).slice(0,20);
  box.innerHTML = matches.length ? matches.map(function(p){
    const label = escTk(p.name)+' ('+escTk(p.part_number)+')';
    const safeLabel = label.replace(/'/g,"\\\\'");
    const row = '<div style="padding:6px 8px;cursor:pointer;font-size:12px;border-bottom:1px solid var(--border)" onclick="selectTicketPart(PID,PLABEL)">'+label+'</div>';
    return row.replace('PID', "'"+p.id+"'").replace('PLABEL', "'"+safeLabel+"'");
  }).join('') : '<div style="padding:6px 8px;font-size:12px;color:var(--muted)">Keine Treffer</div>';
  box.style.display='block';
}
function selectTicketPart(id,label){
  document.getElementById('tp-part-id').value=id;
  document.getElementById('tp-part-selected').textContent='Ausgewählt: '+label;
  document.getElementById('tp-part-results').style.display='none';
  document.getElementById('tp-part-search').value='';
}

async function bookTicketPart(){
  const partId = document.getElementById('tp-part-id').value;
  const locationId = document.getElementById('tp-part-location').value;
  const qty = parseFloat(document.getElementById('tp-part-qty').value);
  const errEl = document.getElementById('tp-part-error');
  errEl.style.display='none';
  if(!partId){ errEl.textContent='Bitte ein Ersatzteil auswählen'; errEl.style.display='block'; return; }
  if(!locationId){ errEl.textContent='Bitte einen Lagerort auswählen'; errEl.style.display='block'; return; }
  if(!qty || qty<=0){ errEl.textContent='Bitte eine gültige Menge angeben'; errEl.style.display='block'; return; }
  try{
    const res = await fetch('/api/v1/tickets/'+TICKET_ID+'/pending-parts', {
      method:'POST', credentials:'same-origin', headers:{'Content-Type':'application/json'},
      body: JSON.stringify({part_id:partId, storage_node_id:locationId, qty:qty})
    });
    const json = await res.json();
    if(!res.ok || json.success===false) throw new Error(json.error || ('HTTP '+res.status));
    closeTicketPartModal();
    loadTicketPartsUsage();
  }catch(e){ errEl.textContent='Fehler: '+e.message; errEl.style.display='block'; }
}

if(document.readyState !== 'loading') initTicketResolveUI();
else document.addEventListener('DOMContentLoaded', initTicketResolveUI);
</script>
{{end}}'''

REPLACEMENTS = [
    ("Neue Karten einfuegen", CARDS_OLD, CARDS_NEW),
    ("JS-Logik ergaenzen", SCRIPTS_OLD, SCRIPTS_NEW),
]


def main():
    if len(sys.argv) != 2:
        print("Aufruf: python3 apply_ticket_resolve_ui_patch.py web/templates/ticket_detail.gohtml")
        sys.exit(1)

    path = sys.argv[1]
    with open(path, "r", encoding="utf-8") as f:
        content = f.read()

    changed = content
    for label, old, new in REPLACEMENTS:
        count = changed.count(old)
        if count == 0:
            print(f"FEHLER: Block '{label}' wurde nicht gefunden. Nichts geändert.")
            sys.exit(1)
        if count > 1:
            print(f"FEHLER: Block '{label}' wurde {count}x gefunden (erwartet 1x). Nichts geändert.")
            sys.exit(1)
        changed = changed.replace(old, new, 1)

    with open(path, "w", encoding="utf-8") as f:
        f.write(changed)
    print(f"OK: {path} gepatcht ({len(REPLACEMENTS)} Stellen ersetzt).")


if __name__ == "__main__":
    main()
