path = "web/templates/shifts.gohtml"
with open(path, encoding="utf-8") as f:
    c = f.read()

start_marker = "<tbody>"
end_marker = "</table>"
start_idx = c.find(start_marker)
end_idx = c.find(end_marker, start_idx)
if start_idx == -1 or end_idx == -1:
    raise SystemExit("FEHLER: tbody/table-Grenzen nicht gefunden")

old_block = c[start_idx:end_idx]

def user_row():
    return '''    <tr>
      <td>
        <div style="font-weight:500;font-size:13px">{{.UserName}}</div>
        <div style="display:flex;flex-direction:column;gap:2px;margin-top:2px">
          {{if .OnCallDuty}}<div style="display:flex;align-items:center;gap:3px;font-size:10px;color:var(--accent)"><i class="ti ti-phone"></i> Bereitschaft{{if .OnCallPhone}} {{.OnCallPhone}}{{end}}</div>{{end}}
          {{if .ShiftLocksmith1}}<div style="display:flex;align-items:center;gap:3px;font-size:10px;color:var(--accent)"><i class="ti ti-tool"></i> Schlosser 1{{if .LocksmithPhone}} {{.LocksmithPhone}}{{end}}</div>{{end}}
          {{if .ShiftLocksmith2}}<div style="display:flex;align-items:center;gap:3px;font-size:10px;color:var(--muted)"><i class="ti ti-tool"></i> Schlosser 2{{if .LocksmithPhone}} {{.LocksmithPhone}}{{end}}</div>{{end}}
          {{if .Sharpening}}<div style="display:flex;align-items:center;gap:3px;font-size:10px;color:var(--accent)"><i class="ti ti-scissors"></i> Schärferei</div>{{end}}
          {{if .HeatingFill}}<div style="display:flex;align-items:center;gap:3px;font-size:10px;color:var(--accent)"><i class="ti ti-flame"></i> Heizungsbefüllung</div>{{end}}
          <div style="display:flex;gap:4px">
            {{if .ShiftLeader}}<i class="ti ti-star" style="font-size:11px;color:var(--amber)" title="Schichtleiter"></i>{{end}}
          </div>
        </div>
      </td>
      {{range $.Days}}
      {{$date := .DateFull}}
      <td style="text-align:center;padding:8px 4px;cursor:pointer" onclick="openAssignPopup('{{$uid}}','{{$date}}',this)">
        {{range $.ShiftMap}}
          {{if and (eq .UserID $uid) (eq .Date $date)}}
          <div class="shift-pill {{.Class}}" style="display:inline-block;min-width:28px;padding:3px 6px">{{.Label}}</div>
          {{end}}
        {{end}}
      </td>
      {{end}}
    </tr>'''

new_block = '''<tbody>
    <tr>
      <td colspan="8" style="background:var(--bg3);padding:8px 12px">
        <div style="display:flex;align-items:center;gap:10px">
          <i class="ti ti-tool" style="color:var(--accent)"></i>
          <span style="font-weight:600;font-size:12px">Schichtschlosser 1</span>
          <input type="tel" class="form-input" style="max-width:180px;font-size:12px" placeholder="Telefonnummer"
            value="{{.LocksmithSlot1Phone}}" onchange="saveLocksmithPhone(1,this.value)">
        </div>
      </td>
    </tr>
    {{$hasSlot1 := false}}
    {{range .Users}}
    {{if .ShiftLocksmith1}}
    {{$hasSlot1 = true}}
    {{$uid := .UserID}}
''' + user_row() + '''
    {{end}}
    {{end}}
    {{if not $hasSlot1}}
    <tr><td colspan="8" style="text-align:center;color:var(--muted);padding:12px;font-size:12px">Kein Mitarbeiter zugeordnet</td></tr>
    {{end}}

    <tr>
      <td colspan="8" style="background:var(--bg3);padding:8px 12px">
        <div style="display:flex;align-items:center;gap:10px">
          <i class="ti ti-tool" style="color:var(--accent)"></i>
          <span style="font-weight:600;font-size:12px">Schichtschlosser 2</span>
          <input type="tel" class="form-input" style="max-width:180px;font-size:12px" placeholder="Telefonnummer"
            value="{{.LocksmithSlot2Phone}}" onchange="saveLocksmithPhone(2,this.value)">
        </div>
      </td>
    </tr>
    {{$hasSlot2 := false}}
    {{range .Users}}
    {{if .ShiftLocksmith2}}
    {{$hasSlot2 = true}}
    {{$uid := .UserID}}
''' + user_row() + '''
    {{end}}
    {{end}}
    {{if not $hasSlot2}}
    <tr><td colspan="8" style="text-align:center;color:var(--muted);padding:12px;font-size:12px">Kein Mitarbeiter zugeordnet</td></tr>
    {{end}}

    <tr>
      <td colspan="8" style="background:var(--bg3);padding:8px 12px">
        <span style="font-weight:600;font-size:12px;color:var(--muted)">Weitere Mitarbeiter</span>
      </td>
    </tr>
    {{$hasOthers := false}}
    {{range .Users}}
    {{if and (not .ShiftLocksmith1) (not .ShiftLocksmith2)}}
    {{$hasOthers = true}}
    {{$uid := .UserID}}
''' + user_row() + '''
    {{end}}
    {{end}}
    {{if not $hasOthers}}
    <tr><td colspan="8" style="text-align:center;color:var(--muted);padding:12px;font-size:12px">Keine weiteren Mitarbeiter</td></tr>
    {{end}}
    {{if not .Users}}
    <tr><td colspan="8" style="text-align:center;color:var(--muted);padding:24px">Keine Schichtzuweisungen diese Woche</td></tr>
    {{end}}
    </tbody>
  </table>'''

c = c[:start_idx] + new_block + c[end_idx+len(end_marker):]

with open(path, "w", encoding="utf-8") as f:
    f.write(c)
print("OK: tbody komplett neu aufgebaut (3 Abschnitte).")


