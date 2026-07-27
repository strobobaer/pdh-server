path = "internal/web/handler.go"
with open(path, encoding="utf-8") as f:
    c = f.read()

old_struct = '''type ShiftsPageData struct {
	BaseData
	WeekNumber int
	WeekRange  string
	PrevWeek   string
	NextWeek   string
	Days       []WeekDay
	Users      []shifts.UserWeekPlan
	ShiftDefs  []ShiftDefView
	Absences   []AbsenceView
	ShiftMap   []ShiftEntry
}'''

new_struct = '''type ShiftsPageData struct {
	BaseData
	WeekNumber          int
	WeekRange           string
	PrevWeek            string
	NextWeek            string
	Days                []WeekDay
	Users               []shifts.UserWeekPlan
	ShiftDefs           []ShiftDefView
	Absences            []AbsenceView
	ShiftMap            []ShiftEntry
	LocksmithSlot1Phone string
	LocksmithSlot2Phone string
	LocksmithSlot1TeamID string
	LocksmithSlot2TeamID string
}'''

if old_struct not in c:
    raise SystemExit("FEHLER: ShiftsPageData nicht gefunden")
c = c.replace(old_struct, new_struct, 1)

anchor = '''	if models, err := h.shifts.ListModels(ctx); err == nil && len(models) > 0 {'''
if anchor not in c:
    raise SystemExit("FEHLER: Anker fuer Locksmith-Zuweisungen nicht gefunden")

addition = '''	if assignments, err := h.shifts.GetLocksmithAssignments(ctx); err == nil {
		for _, a := range assignments {
			if a.Slot == 1 {
				data.LocksmithSlot1Phone = a.Phone
				if a.TeamID != nil {
					data.LocksmithSlot1TeamID = *a.TeamID
				}
			} else if a.Slot == 2 {
				data.LocksmithSlot2Phone = a.Phone
				if a.TeamID != nil {
					data.LocksmithSlot2TeamID = *a.TeamID
				}
			}
		}
	}

''' + anchor

c = c.replace(anchor, addition, 1)

with open(path, "w", encoding="utf-8") as f:
    f.write(c)
print("OK: ShiftsPageData um Locksmith-Zuweisungen erweitert.")

