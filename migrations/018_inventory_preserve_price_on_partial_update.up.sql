-- 018_inventory_preserve_price_on_partial_update.up.sql
-- Verhindert, dass Teilbearbeitung ohne Preisfeld den vorhandenen Preis auf 0 setzt.

CREATE OR REPLACE FUNCTION preserve_spare_part_price_on_partial_update()
RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.price = 0 AND OLD.price > 0 THEN
        NEW.price := OLD.price;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_preserve_spare_part_price_on_partial_update ON spare_parts;

CREATE TRIGGER trg_preserve_spare_part_price_on_partial_update
BEFORE UPDATE ON spare_parts
FOR EACH ROW
EXECUTE FUNCTION preserve_spare_part_price_on_partial_update();
