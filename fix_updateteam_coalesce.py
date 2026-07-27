path = "internal/core/shifts/teams.go"
with open(path, encoding="utf-8") as f:
    c = f.read()

old = '''func (r *Repository) UpdateTeam(ctx context.Context, id, name, phone string) error {
	_, err := r.db.Exec(ctx, `UPDATE shift_teams SET name=$1, phone=$2 WHERE id=$3`, name, phone, id)
	return err
}'''

new = '''func (r *Repository) UpdateTeam(ctx context.Context, id, name, phone string) error {
	_, err := r.db.Exec(ctx, `UPDATE shift_teams SET name=COALESCE(NULLIF($1,''), name), phone=$2 WHERE id=$3`, name, phone, id)
	return err
}'''

if old not in c:
    raise SystemExit("FEHLER: Repository.UpdateTeam nicht gefunden")
c = c.replace(old, new, 1)

with open(path, "w", encoding="utf-8") as f:
    f.write(c)
print("OK: UpdateTeam - leerer Name ueberschreibt nicht mehr.")

