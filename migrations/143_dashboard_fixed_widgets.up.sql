-- 143_dashboard_fixed_widgets.up.sql
-- Die frueher fest eingebauten Teile des Dashboards (Tabelle, Zeitstrahl,
-- Aktive Stoerungen, Schichtplan, Wartung faellig) sind jetzt Widgets
-- (internal/web/dashboard_widgets.go). Wer schon ein eigenes Dashboard oder
-- einen Firmenstandard gespeichert hat, bekommt sie einmalig angehaengt,
-- damit nichts verschwindet. Danach lassen sie sich frei verschieben oder
-- entfernen.

CREATE OR REPLACE FUNCTION pdh_tmp_add_fixed_widgets(list JSONB) RETURNS JSONB AS $$
DECLARE
    w RECORD;
BEGIN
    IF list IS NULL OR jsonb_typeof(list) <> 'array' THEN
        RETURN list;
    END IF;
    FOR w IN SELECT * FROM (VALUES
        ('m143a', 'vg_table', 4), ('m143b', 'vg_timeline', 4), ('m143c', 'list_faults', 2),
        ('m143d', 'my_shifts', 2), ('m143e', 'list_maintenance', 4)) AS t(id, type, size)
    LOOP
        IF NOT list @> jsonb_build_array(jsonb_build_object('type', w.type)) THEN
            list := list || jsonb_build_array(jsonb_build_object('id', w.id, 'type', w.type, 'size', w.size));
        END IF;
    END LOOP;
    RETURN list;
END $$ LANGUAGE plpgsql;

UPDATE users SET dashboard_widgets = pdh_tmp_add_fixed_widgets(dashboard_widgets)
WHERE dashboard_widgets IS NOT NULL;

UPDATE app_settings SET value = pdh_tmp_add_fixed_widgets(value::jsonb)::text, updated_at = NOW()
WHERE key = 'defaults.dashboard_widgets' AND value LIKE '[%';

DROP FUNCTION pdh_tmp_add_fixed_widgets(JSONB);
