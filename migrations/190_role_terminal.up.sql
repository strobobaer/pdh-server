-- 190_role_terminal.up.sql
--
-- Feste Rolle „Terminal“ (Schlüssel terminal) für den Systembenutzer eines
-- Terminals bzw. Infobildschirms: ansehen und melden (anlegen) von Tickets,
-- Störungen und Rüstzeiten, sonst nichts – kein Lösen, kein Löschen, keine
-- Administration. Die Rechte gelten zunächst überall (department_id NULL, also
-- als Vorlage); der Betreiber begrenzt sie auf eine Abteilung (samt
-- Unterabteilungen), siehe Handbuch „Terminal“. Es gibt kein eigenes
-- „anlegen“-Recht, Anlegen und Ändern hängen an *.edit.
-- Rein hinzufügend: bestehende Rollen und Rechte bleiben unberührt.

INSERT INTO roles (key, label, is_builtin, level) VALUES ('terminal', 'Terminal', true, 10)
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.key = 'terminal'
  AND p.key IN ('tickets.view', 'tickets.edit', 'faults.view', 'faults.edit', 'setup.view', 'setup.edit')
ON CONFLICT DO NOTHING;
