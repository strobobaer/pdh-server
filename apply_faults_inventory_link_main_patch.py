#!/usr/bin/env python3
"""
Patcht cmd/server/main.go: verbindet das faults-Modul mit dem
Lager-Service, damit vorgemerkte Ersatzteile beim Lösen einer Störung
tatsächlich gebucht werden koennen.

Aufruf:
    python3 apply_faults_inventory_link_main_patch.py cmd/server/main.go
"""
import sys

OLD = "invHandler := inventory.NewHandler(invSvc)"
NEW = "invHandler := inventory.NewHandler(invSvc)\n\tfaults.SetInventoryService(invSvc)"


def main():
    if len(sys.argv) != 2:
        print("Aufruf: python3 apply_faults_inventory_link_main_patch.py cmd/server/main.go")
        sys.exit(1)

    path = sys.argv[1]
    with open(path, "r", encoding="utf-8") as f:
        content = f.read()

    count = content.count(OLD)
    if count == 0:
        print("FEHLER: Anker nicht gefunden. Nichts geändert.")
        sys.exit(1)
    if count > 1:
        print(f"FEHLER: Anker {count}x gefunden (erwartet 1x). Nichts geändert.")
        sys.exit(1)

    content = content.replace(OLD, NEW, 1)
    with open(path, "w", encoding="utf-8") as f:
        f.write(content)
    print(f"OK: {path} gepatcht.")


if __name__ == "__main__":
    main()
