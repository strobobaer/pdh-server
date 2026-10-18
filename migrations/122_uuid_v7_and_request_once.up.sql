-- 122_uuid_v7_and_request_once.up.sql
--
-- 1) UUIDv7: zeitlich sortierte IDs (48 Bit Millisekunden-Zeitstempel + 74 Bit
--    Zufall, RFC 9562). Eindeutig waren die IDs schon vorher (UUIDv4 +
--    Primärschlüssel); v7 sortiert neue Datensätze ans Ende des Index und
--    zeigt die Entstehungszeit. PostgreSQL 16 hat noch kein uuidv7(), daher
--    eine eigene Funktion; alle Spalten mit DEFAULT gen_random_uuid() werden
--    umgestellt. Bestehende IDs bleiben unverändert.
CREATE OR REPLACE FUNCTION pdh_uuid_v7() RETURNS uuid
LANGUAGE sql VOLATILE PARALLEL SAFE AS $$
    SELECT encode(
        set_bit(set_bit(
            overlay(uuid_send(gen_random_uuid())
                    PLACING substring(int8send(floor(extract(epoch FROM clock_timestamp()) * 1000)::bigint) FROM 3)
                    FROM 1 FOR 6),
            52, 1), 53, 1),
        'hex')::uuid
$$;

DO $$
DECLARE c record;
BEGIN
    FOR c IN
        SELECT table_schema, table_name, column_name
        FROM information_schema.columns
        WHERE table_schema = 'public' AND data_type = 'uuid'
          AND column_default ILIKE '%gen_random_uuid()%'
    LOOP
        EXECUTE format('ALTER TABLE %I.%I ALTER COLUMN %I SET DEFAULT pdh_uuid_v7()', c.table_schema, c.table_name, c.column_name);
    END LOOP;
END $$;

-- 2) Doppel-Absende-Sperre (internal/web/request_once.go): jedes Absenden
--    eines Formulars bzw. jede htmx-Aktion trägt einen einmaligen
--    UUIDv7-Schlüssel; derselbe Schlüssel wird nur einmal ausgeführt.
CREATE TABLE IF NOT EXISTS request_once (
    token      UUID PRIMARY KEY,
    user_id    UUID,
    path       TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_request_once_created ON request_once(created_at);
