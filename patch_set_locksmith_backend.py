path = "internal/core/users/repository.go"
with open(path, encoding="utf-8") as f:
    c = f.read()

sig = "func (r *Repository) Deactivate(ctx context.Context, id string) error {"
if sig not in c:
    raise SystemExit("FEHLER: Deactivate nicht gefunden - zeig mir 'grep -n \"func (r \\*Repository)\" internal/core/users/repository.go'")
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

addition = '''

// SetLocksmithSlot setzt genau einen Nutzer als Schichtschlosser 1 oder 2
// (slot=1 oder 2) und setzt das Flag bei allen anderen Nutzern zurueck,
// damit die Rolle immer eindeutig einer Person zugeordnet ist.
func (r *Repository) SetLocksmithSlot(ctx context.Context, slot int, userID string) error {
	column := "shift_locksmith_1"
	if slot == 2 {
		column = "shift_locksmith_2"
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "UPDATE users SET "+column+"=false WHERE "+column+"=true"); err != nil {
		return err
	}
	if userID != "" {
		if _, err := tx.Exec(ctx, "UPDATE users SET "+column+"=true WHERE id=$1", userID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}'''

c = c[:end] + addition + c[end:]

with open(path, "w", encoding="utf-8") as f:
    f.write(c)
print("OK: SetLocksmithSlot ergaenzt.")


