#!/usr/bin/env python3
"""
Patcht cmd/server/main.go: verbindet das tickets-Modul mit dem Lager-
Service (analog zu faults.SetInventoryService).

Aufruf (NACH apply_faults_inventory_link_main_patch.py ausfuehren, oder
unabhaengig - beide suchen unterschiedliche Anker):
    python3 apply_tickets_inventory_link_main_patch.py cmd/server/main.go
"""
import sys

OLD = "invHandler := inventory.NewHandler(invSvc)"


def main():
    if len(sys.argv) != 2:
        print("Aufruf: python3 apply_tickets_inventory_link_main_patch.py cmd/server/main.go")
        sys.exit(1)

    path = sys.argv[1]
    with open(path, "r", encoding="utf-8") as f:
        content = f.read()

    if OLD not in content:
        print("FEHLER: Anker nicht gefunden. Nichts geändert.")
        sys.exit(1)

    already = "tickets.SetInventoryService(invSvc)" in content
    if already:
        print("OK: bereits vorhanden, nichts zu tun.")
        return

    new = OLD + "\n\ttickets.SetInventoryService(invSvc)"
    content = content.replace(OLD, new, 1)
    with open(path, "w", encoding="utf-8") as f:
        f.write(content)
    print(f"OK: {path} gepatcht.")


if __name__ == "__main__":
    main()
