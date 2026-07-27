path = "internal/web/handler.go"
with open(path, encoding="utf-8") as f:
    c = f.read()

old_struct = '''	LocksmithSlot1Phone string
	LocksmithSlot2Phone string
	LocksmithSlot1TeamID string
	LocksmithSlot2TeamID string
}'''

new_struct = '''	LocksmithSlot1Phone string
	LocksmithSlot2Phone string
	LocksmithSlot1TeamID string
	LocksmithSlot2TeamID string
	LocksmithSlot1UserID string
	LocksmithSlot2UserID string
	UserOptions          []UserOption
}'''

if old_struct not in c:
    raise SystemExit("FEHLER: ShiftsPageData-Ende nicht gefunden")
c = c.replace(old_struct, new_struct, 1)

anchor = '''	if assignments, err := h.shifts.GetLocksmithAssignments(ctx); err == nil {'''
if anchor not in c:
    raise SystemExit("FEHLER: Locksmith-Anker nicht gefunden")

addition = '''	data.UserOptions = h.userOptions(ctx)
	for _, u := range data.Users {
		if u.ShiftLocksmith1 {
			data.LocksmithSlot1UserID = u.UserID
		}
		if u.ShiftLocksmith2 {
			data.LocksmithSlot2UserID = u.UserID
		}
	}

''' + anchor

c = c.replace(anchor, addition, 1)

with open(path, "w", encoding="utf-8") as f:
    f.write(c)
print("OK: ShiftsPageData um UserOptions + aktuelle Slot-Belegung erweitert.")


