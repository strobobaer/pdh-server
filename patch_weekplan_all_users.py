path = "internal/core/shifts/plans.go"
with open(path, encoding="utf-8") as f:
    c = f.read()

old = '''	var users []UserWeekPlan
	for _, u := range userMap {
		users = append(users, *u)
	}'''

new = '''	// Alle aktiven Mitarbeiter ergaenzen, die noch keine Zuweisung diese
	// Woche haben, damit sie in der Tabelle sichtbar bleiben (z.B. um sie
	// als Schichtschlosser zuzuweisen, auch ohne bereits eine Schicht zu haben).
	for id, q := range quals {
		if _, ok := userMap[id]; ok {
			continue
		}
		uwp := &UserWeekPlan{UserID: id, UserName: q.UserName, Days: make(map[string]DayEntry), TeamSortOrder: 3}
		uwp.OnCallDuty = q.OnCallDuty
		uwp.OnCallPhone = q.Phone
		uwp.ShiftLocksmith1 = q.ShiftLocksmith1
		uwp.ShiftLocksmith2 = q.ShiftLocksmith2
		uwp.Sharpening = q.Sharpening
		uwp.HeatingFill = q.HeatingFill
		uwp.ShiftLeader = q.ShiftLeader
		if q.ShiftLocksmith1 {
			uwp.LocksmithPhone = slot1Phone
			uwp.TeamSortOrder = 1
		} else if q.ShiftLocksmith2 {
			uwp.LocksmithPhone = slot2Phone
			uwp.TeamSortOrder = 2
		}
		userMap[id] = uwp
	}

	var users []UserWeekPlan
	for _, u := range userMap {
		users = append(users, *u)
	}'''

if old not in c:
    raise SystemExit("FEHLER: users-append-Block nicht gefunden")
c = c.replace(old, new, 1)

with open(path, "w", encoding="utf-8") as f:
    f.write(c)
print("OK: Alle aktiven Mitarbeiter werden jetzt ergaenzt.")


