-- 185_projects_edit_label.up.sql
-- projects.edit ist seit 148 das Recht zum Anlegen und Bearbeiten von Projekten
-- (Rechte-Regel in internal/web/perm_gate.go, Knopf „Neues Projekt“). Die
-- Beschriftung aus 144 nannte nur die Zusatzfelder. Nur die alte Standard-
-- Beschriftung wird ersetzt; eine von Hand geänderte bleibt.
UPDATE permissions SET label = 'Projekte bearbeiten'
 WHERE key = 'projects.edit' AND label = 'Projekte: Zusatzfelder (Feldsätze) bearbeiten';
