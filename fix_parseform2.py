path = "cmd/server/main.go"
with open(path, encoding="utf-8") as f:
    lines = f.readlines()

target_line_idx = None
for i, line in enumerate(lines):
    if "r.ParseForm()" in line and i > 0 and "edit-web" in lines[i-1]:
        target_line_idx = i
        break

if target_line_idx is None:
    raise SystemExit("FEHLER: Zeile mit r.ParseForm() direkt nach edit-web nicht gefunden")

old_line = lines[target_line_idx]
new_line = old_line.replace("r.ParseForm()", "r.ParseMultipartForm(32 << 20)")
lines[target_line_idx] = new_line

with open(path, "w", encoding="utf-8") as f:
    f.writelines(lines)

print("OK: Zeile " + str(target_line_idx+1) + " gepatcht.")
print("Vorher: " + old_line.strip())
print("Nachher: " + new_line.strip())

