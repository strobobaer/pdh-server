// Ersatzteil-Reservierung: (teilweise) zurueck an den Lagerplatz buchen.
// reload: Funktion zum Neuladen der Liste (sonst ganze Seite).
async function pdhReturnReservation(kind, ref, id, max, reload){
  const v = prompt(pdhT("Wie viel zurückbuchen? (reserviert: %s)").replace('#', max), String(max));
  if(v === null) return;
  const qty = parseFloat(String(v).replace(',', '.'));
  if(!(qty > 0)){ alert(pdhT("Bitte eine Menge größer 0 angeben.")); return; }
  try{
    const res = await fetch('/reservations/' + encodeURIComponent(kind) + '/' + encodeURIComponent(ref) + '/' + encodeURIComponent(id) + '/return',
      {method: 'POST', credentials: 'same-origin', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({qty: Math.min(qty, max)})});
    const j = await res.json();
    if(!res.ok || j.success === false) throw new Error(j.error || ('HTTP ' + res.status));
    if(typeof reload === 'function') reload(); else location.reload();
  }catch(e){ alert(pdhT("Fehler") + ': ' + e.message); }
}
// Reservierung als verbraucht buchen (Anlagen-Reservierungen, siehe part_booking.go).
async function pdhConsumeReservation(kind, ref, id){
  if(!confirm(pdhT("Reservierte Menge als verbraucht buchen?"))) return;
  try{
    const res = await fetch('/reservations/' + encodeURIComponent(kind) + '/' + encodeURIComponent(ref) + '/' + encodeURIComponent(id) + '/consume',
      {method: 'POST', credentials: 'same-origin'});
    const j = await res.json();
    if(!res.ok || j.success === false) throw new Error(j.error || ('HTTP ' + res.status));
    location.reload();
  }catch(e){ alert(pdhT("Fehler") + ': ' + e.message); }
}
// Kennzeichnung + Rueckbuchen-Knopf fuer eine vorgemerkte Position (p aus .../pending-parts)
function pdhReservedTag(p, kind, ref, reloadName){
  if(!p || !p.reserved) return '';
  const a = [kind, ref, p.id].map(x => "'" + String(x).replace(/[^\w-]/g, '') + "'").join(',');
  return ' · <span class="res-q" title="' + pdhT("bereits vom Lager abgebucht") + '">' + pdhT("reserviert") + '</span>'
    + ' <button type="button" class="btn btn-small" style="font-size:11px;padding:2px 8px" title="' + pdhT("Nicht benötigte Menge zurück an den Lagerplatz buchen") + '" onclick="pdhReturnReservation(' + a + ',' + Number(p.qty) + ',' + reloadName + ')"><i class="ti ti-arrow-back-up"></i> ' + pdhT("Zurückbuchen") + '</button>';
}
