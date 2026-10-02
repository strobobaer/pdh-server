-- Broker auch fuer Aufgaben und Wartungen: Im Leitstand angelegte Vorgaenge
-- werden nie zugewiesen, sondern an die Broker des jeweiligen Typs verteilt.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS broker_tasks       BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS broker_maintenance BOOLEAN NOT NULL DEFAULT false;
