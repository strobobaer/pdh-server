path = "internal/core/shifts/plans.go"
with open(path, encoding="utf-8") as f:
    c = f.read()

old_struct = '''type UserQualInfo struct {
	Phone           string
	OnCallDuty      bool
	ShiftLocksmith1 bool
	ShiftLocksmith2 bool
	Sharpening      bool
	HeatingFill     bool
	ShiftLeader     bool
}'''

new_struct = '''type UserQualInfo struct {
	UserName        string
	Phone           string
	OnCallDuty      bool
	ShiftLocksmith1 bool
	ShiftLocksmith2 bool
	Sharpening      bool
	HeatingFill     bool
	ShiftLeader     bool
}'''

if old_struct not in c:
    raise SystemExit("FEHLER: UserQualInfo-Struct nicht gefunden")
c = c.replace(old_struct, new_struct, 1)

old_query = '''func (r *Repository) GetUserQualifications(ctx context.Context) (map[string]*UserQualInfo, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id::text, COALESCE(phone,''), on_call_duty, shift_locksmith_1, shift_locksmith_2,
			sharpening, heating_fill, shift_leader
		FROM users WHERE active=true`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*UserQualInfo{}
	for rows.Next() {
		var id string
		q := &UserQualInfo{}
		if err := rows.Scan(&id, &q.Phone, &q.OnCallDuty, &q.ShiftLocksmith1, &q.ShiftLocksmith2,
			&q.Sharpening, &q.HeatingFill, &q.ShiftLeader); err != nil {
			return nil, err
		}
		out[id] = q
	}
	return out, rows.Err()
}'''

new_query = '''func (r *Repository) GetUserQualifications(ctx context.Context) (map[string]*UserQualInfo, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id::text, first_name || ' ' || last_name, COALESCE(phone,''), on_call_duty, shift_locksmith_1, shift_locksmith_2,
			sharpening, heating_fill, shift_leader
		FROM users WHERE active=true`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*UserQualInfo{}
	for rows.Next() {
		var id string
		q := &UserQualInfo{}
		if err := rows.Scan(&id, &q.UserName, &q.Phone, &q.OnCallDuty, &q.ShiftLocksmith1, &q.ShiftLocksmith2,
			&q.Sharpening, &q.HeatingFill, &q.ShiftLeader); err != nil {
			return nil, err
		}
		out[id] = q
	}
	return out, rows.Err()
}'''

if old_query not in c:
    raise SystemExit("FEHLER: GetUserQualifications-Query nicht gefunden")
c = c.replace(old_query, new_query, 1)

with open(path, "w", encoding="utf-8") as f:
    f.write(c)
print("OK: UserQualInfo um UserName erweitert.")


