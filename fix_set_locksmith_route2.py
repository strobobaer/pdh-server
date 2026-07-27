path = "internal/core/users/handler.go"
with open(path, encoding="utf-8") as f:
    lines = f.readlines()

for i, line in enumerate(lines):
    if 'r.Post("/{id}", h.Update)' in line:
        indent = line[:len(line) - len(line.lstrip())]
        lines.insert(i+1, indent + 'r.Post("/set-locksmith/{slot}", h.SetLocksmithSlot)\n')
        print("OK: Route nach Zeile " + str(i+1) + " eingefuegt.")
        break
else:
    raise SystemExit("FEHLER: Zeile nicht gefunden")

with open(path, "w", encoding="utf-8") as f:
    f.writelines(lines)


