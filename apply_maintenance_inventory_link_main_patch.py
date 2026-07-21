#!/usr/bin/env python3
"""
Patcht cmd/server/main.go: verbindet das maintenance-Modul mit dem
Lager-Service (analog zu faults/tickets.SetInventoryService).

Aufruf:
    python3 apply_maintenance_inventory_link_main_patch.py cmd/server/main.go
"""
import sys

OLD = "invHandler := inventory.NewHandler(invSvc)"


def main():
    if len(sys.argv) != 2:
        print("Aufruf: python3 apply_maintenance_inventory_link_main_patch.py cmd/server/main.go")
        sys.exit(1)

    path = sys.argv[1]
    with open(path, "r", encoding="utf-8") as f:
        content = f.read()

    if OLD not in content:
        print("FEHLER: Anker nicht gefunden. Nichts geändert.")
        sys.exit(1)

    if "maintenance.SetInventoryService(invSvc)" in content:
        print("OK: bereits vorhanden, nichts zu tun.")
        return

    new = OLD + "\n\tmaintenance.SetInventoryService(invSvc)"
    content = content.replace(OLD, new, 1)
    with open(path, "w", encoding="utf-8") as f:
        f.write(content)
    print(f"OK: {path} gepatcht.")


if __name__ == "__main__":
    main()
