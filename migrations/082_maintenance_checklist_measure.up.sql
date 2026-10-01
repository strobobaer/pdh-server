-- 082_maintenance_checklist_measure.up.sql
-- Wartungs-Checklisten: Messwerte mit optionalem Soll-, Min- und Max-Wert
-- (NULL = nicht aktiviert) und Einheit; Ergebnis merkt sich, ob der Messwert
-- innerhalb von Min/Max lag. Bilder haengen als Anhaenge an Vorlagenpunkt
-- (ref_type 'maint_check_item', Darstellung) bzw. Ergebnis
-- (ref_type 'maint_check_result', Dokumentation).

ALTER TABLE maintenance_checklist_template_items
    ADD COLUMN IF NOT EXISTS unit         TEXT    NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS target_value NUMERIC NULL,
    ADD COLUMN IF NOT EXISTS min_value    NUMERIC NULL,
    ADD COLUMN IF NOT EXISTS max_value    NUMERIC NULL;

ALTER TABLE maintenance_task_checklist_results
    ADD COLUMN IF NOT EXISTS in_range BOOLEAN NULL;
