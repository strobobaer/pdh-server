-- 158_extsync.up.sql
-- Abgleich mit Fremdsystemen (Paket 1): Tickets und Störmeldungen werden mit einer
-- Tabelle in einer fremden Datenbank abgeglichen (herein und hinaus).
-- Optionales Merkmal: Ohne Profil bleiben alle Tabellen leer, die Auslöser tun
-- dann nichts und am Verhalten von PDH ändert sich nichts.

-- ── Profile: eine Zuordnung Fremdtabelle <-> Ticket/Störmeldung ──────────
CREATE TABLE IF NOT EXISTS sync_profiles (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name           TEXT NOT NULL,
    connection_id  UUID NOT NULL REFERENCES import_export_connections(id) ON DELETE RESTRICT,
    department_id  UUID REFERENCES departments(id) ON DELETE SET NULL,
    target_type    TEXT NOT NULL CHECK (target_type IN ('ticket', 'fault')),
    source_schema  TEXT NOT NULL DEFAULT '',
    source_table   TEXT NOT NULL,
    key_columns    TEXT[] NOT NULL,
    change_column  TEXT,
    change_kind    TEXT NOT NULL DEFAULT 'none' CHECK (change_kind IN ('none', 'timestamp', 'rowversion')),
    filter_sql     TEXT NOT NULL DEFAULT '',
    window_days    INT NOT NULL DEFAULT 0,
    defaults       JSONB NOT NULL DEFAULT '{}'::jsonb,
    comment_table  JSONB,
    interval_minutes INT NOT NULL DEFAULT 5,
    max_new        INT NOT NULL DEFAULT 200,
    state          TEXT NOT NULL DEFAULT 'entwurf' CHECK (state IN ('entwurf', 'getestet', 'scharf', 'angehalten')),
    state_note     TEXT NOT NULL DEFAULT '',
    test_dry_at    TIMESTAMPTZ,
    test_write_at  TIMESTAMPTZ,
    cursor         TEXT NOT NULL DEFAULT '',
    fail_count     INT NOT NULL DEFAULT 0,
    next_run_at    TIMESTAMPTZ,
    created_by     UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_sync_profiles_due ON sync_profiles (next_run_at) WHERE state = 'scharf';

-- ── Feldzuordnung je Profil ───────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS sync_field_maps (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    profile_id     UUID NOT NULL REFERENCES sync_profiles(id) ON DELETE CASCADE,
    foreign_column TEXT NOT NULL DEFAULT '',
    pdh_field      TEXT NOT NULL,
    direction      TEXT NOT NULL DEFAULT 'herein' CHECK (direction IN ('herein', 'hinaus', 'beides')),
    translate      JSONB NOT NULL DEFAULT '{}'::jsonb,
    fixed_value    TEXT,
    position       INT NOT NULL DEFAULT 0,
    UNIQUE (profile_id, pdh_field)
);

-- ── Verknüpfungen: Fremdzeile <-> PDH-Datensatz ──────────────────────────
-- ref_id NULL = reserviert, der Datensatz ist noch nicht angelegt.
CREATE TABLE IF NOT EXISTS sync_links (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    profile_id     UUID NOT NULL REFERENCES sync_profiles(id) ON DELETE CASCADE,
    foreign_key    TEXT NOT NULL,
    ref_type       TEXT NOT NULL CHECK (ref_type IN ('ticket', 'fault')),
    ref_id         UUID,
    seen_mark      TEXT NOT NULL DEFAULT '',
    seen_values    JSONB NOT NULL DEFAULT '{}'::jsonb,
    written_values JSONB NOT NULL DEFAULT '{}'::jsonb,
    last_error     TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (profile_id, foreign_key)
);
-- Dieser Index trägt auch die Prüfung im Auslöser (eine Suche je Änderung).
CREATE INDEX IF NOT EXISTS idx_sync_links_ref ON sync_links (ref_type, ref_id);

-- ── Warteliste: Änderungen aus PDH, die ins Fremdsystem geschrieben werden ─
CREATE TABLE IF NOT EXISTS sync_outbox (
    id          BIGSERIAL PRIMARY KEY,
    link_id     UUID NOT NULL REFERENCES sync_links(id) ON DELETE CASCADE,
    kind        TEXT NOT NULL CHECK (kind IN ('update', 'comment')),
    payload     JSONB NOT NULL,
    attempts    INT NOT NULL DEFAULT 0,
    last_error  TEXT NOT NULL DEFAULT '',
    next_try_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    done_at     TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_sync_outbox_open ON sync_outbox (link_id, id) WHERE done_at IS NULL;

-- ── Läufe (Protokoll) ─────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS sync_runs (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    profile_id     UUID NOT NULL REFERENCES sync_profiles(id) ON DELETE CASCADE,
    direction      TEXT NOT NULL CHECK (direction IN ('herein', 'hinaus', 'test')),
    started_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at    TIMESTAMPTZ,
    n_new          INT NOT NULL DEFAULT 0,
    n_changed      INT NOT NULL DEFAULT 0,
    n_written      INT NOT NULL DEFAULT 0,
    n_failed       INT NOT NULL DEFAULT 0,
    skipped_reason TEXT NOT NULL DEFAULT '',
    errors         JSONB NOT NULL DEFAULT '[]'::jsonb
);
CREATE INDEX IF NOT EXISTS idx_sync_runs_profile ON sync_runs (profile_id, started_at DESC);

-- ── Auslöser: PDH-Änderungen in die Warteliste ────────────────────────────
-- Eine Funktion für alle vier Fälle (TG_ARGV[0]):
--   'ticket' / 'fault'      AFTER UPDATE auf tickets / faults
--   'ticket_comment'        AFTER INSERT auf ticket_comments
--   'infra_comment'         AFTER INSERT auf infrastructure_comments
-- Es entsteht je Verknüpfung (Profil) ein Eintrag, nur wenn
--   * der Datensatz verknüpft ist (eine Suche über idx_sync_links_ref),
--   * die Änderung nicht vom Abgleich selbst kommt (pdh.sync_origin = '1').
--
-- Sicherheit: Das Speichern eines Tickets darf nie am Abgleich scheitern. Die
-- Prüfung "gibt es eine Verknüpfung" ist eine einzelne Indexabfrage und liegt
-- bewusst vor dem Fehlerblock – so kostet der Normalfall (keine Verknüpfung)
-- keine Unterabfrage-Transaktion. Alles, was rechnet oder schreibt, steht
-- im Block mit EXCEPTION WHEN OTHERS: Fehler werden als Warnung gemeldet und
-- die Änderung am Ticket läuft trotzdem durch.
CREATE OR REPLACE FUNCTION pdh_sync_enqueue() RETURNS trigger AS $$
DECLARE
    rtype  TEXT;
    rid    UUID;
    fields JSONB;
    -- Spalten, die ins Fremdsystem gehen können; fehlende Spalten
    -- (priority bei faults, severity bei tickets) werden übergangen.
    cols   CONSTANT TEXT[] := ARRAY['title', 'description', 'priority', 'severity',
                                    'status', 'due_date', 'resolution', 'root_cause'];
BEGIN
    IF current_setting('pdh.sync_origin', true) = '1' THEN
        RETURN NEW;
    END IF;

    IF TG_ARGV[0] IN ('ticket', 'fault') THEN
        rtype := TG_ARGV[0];
        rid := NEW.id;
    ELSIF TG_ARGV[0] = 'ticket_comment' THEN
        rtype := 'ticket';
        rid := NEW.ticket_id;
    ELSIF TG_ARGV[0] = 'infra_comment' THEN
        IF NEW.ref_type IS NULL OR NEW.ref_type NOT IN ('ticket', 'fault') THEN
            RETURN NEW;
        END IF;
        rtype := NEW.ref_type;
        rid := NEW.ref_id;
    ELSE
        RETURN NEW;
    END IF;
    IF rid IS NULL THEN
        RETURN NEW;
    END IF;

    -- Normalfall: keine Verknüpfung, nichts weiter zu tun.
    IF NOT EXISTS (SELECT 1 FROM sync_links WHERE ref_type = rtype AND ref_id = rid) THEN
        RETURN NEW;
    END IF;

    BEGIN
        IF TG_ARGV[0] IN ('ticket', 'fault') THEN
            -- Nur geänderte Spalten aus der Liste, soweit die Tabelle sie hat.
            SELECT jsonb_object_agg(nv.key, nv.value) INTO fields
              FROM jsonb_each(to_jsonb(NEW)) nv
              JOIN jsonb_each(to_jsonb(OLD)) ov ON ov.key = nv.key
             WHERE nv.key = ANY (cols)
               AND nv.value IS DISTINCT FROM ov.value;
            IF fields IS NULL THEN
                RETURN NEW;
            END IF;
            INSERT INTO sync_outbox (link_id, kind, payload)
            SELECT l.id, 'update', jsonb_build_object('fields', fields)
              FROM sync_links l
             WHERE l.ref_type = rtype AND l.ref_id = rid;
        ELSE
            INSERT INTO sync_outbox (link_id, kind, payload)
            SELECT l.id, 'comment', jsonb_build_object(
                       'text', NEW.text,
                       'user_id', COALESCE(to_jsonb(NEW)->'user_id', to_jsonb(NEW)->'created_by'),
                       'created_at', NEW.created_at)
              FROM sync_links l
             WHERE l.ref_type = rtype AND l.ref_id = rid;
        END IF;
    EXCEPTION WHEN OTHERS THEN
        RAISE WARNING 'pdh_sync_enqueue: % (%)', SQLERRM, SQLSTATE;
    END;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_pdh_sync_tickets ON tickets;
CREATE TRIGGER trg_pdh_sync_tickets AFTER UPDATE ON tickets
    FOR EACH ROW EXECUTE FUNCTION pdh_sync_enqueue('ticket');
DROP TRIGGER IF EXISTS trg_pdh_sync_faults ON faults;
CREATE TRIGGER trg_pdh_sync_faults AFTER UPDATE ON faults
    FOR EACH ROW EXECUTE FUNCTION pdh_sync_enqueue('fault');
DROP TRIGGER IF EXISTS trg_pdh_sync_ticket_comments ON ticket_comments;
CREATE TRIGGER trg_pdh_sync_ticket_comments AFTER INSERT ON ticket_comments
    FOR EACH ROW EXECUTE FUNCTION pdh_sync_enqueue('ticket_comment');
DROP TRIGGER IF EXISTS trg_pdh_sync_infra_comments ON infrastructure_comments;
CREATE TRIGGER trg_pdh_sync_infra_comments AFTER INSERT ON infrastructure_comments
    FOR EACH ROW EXECUTE FUNCTION pdh_sync_enqueue('infra_comment');

-- ── Rechte ────────────────────────────────────────────────────────────────
INSERT INTO permissions (key, label, category) VALUES
    ('sync.view',  'Abgleich: Profile, Läufe und Herkunft sehen', 'Daten & Schnittstellen'),
    ('sync.edit',  'Abgleich: Profile einrichten und testen', 'Daten & Schnittstellen'),
    ('sync.run',   'Abgleich: scharf schalten, anhalten, erneut versuchen', 'Daten & Schnittstellen'),
    ('sync.admin', 'Abgleich: Profile löschen (Bereichsadministrator)', 'Daten & Schnittstellen')
ON CONFLICT (key) DO NOTHING;

-- Wer bisher Import verwalten darf (systemweit), darf den Abgleich einrichten –
-- niemand wird durch das Update ausgesperrt, niemand bekommt mehr als nötig.
INSERT INTO role_permissions (role_id, permission_id)
SELECT DISTINCT r.id, p.id FROM roles r
CROSS JOIN permissions p
WHERE p.key IN ('sync.view', 'sync.edit', 'sync.run')
  AND EXISTS (
        SELECT 1 FROM role_permissions rp JOIN permissions o ON o.id = rp.permission_id
        WHERE rp.role_id = r.id AND rp.department_id IS NULL AND o.key = 'import.write')
ON CONFLICT DO NOTHING;

-- Bereichsadministrator: Rolle admin und Rollen, die Import verwalten dürfen
-- und schon in einem anderen Bereich Bereichsadministrator sind.
INSERT INTO role_permissions (role_id, permission_id)
SELECT DISTINCT r.id, p.id FROM roles r
CROSS JOIN permissions p
WHERE p.key = 'sync.admin'
  AND (r.key = 'admin'
       OR (EXISTS (
               SELECT 1 FROM role_permissions rp JOIN permissions o ON o.id = rp.permission_id
               WHERE rp.role_id = r.id AND rp.department_id IS NULL AND o.key = 'import.write')
           AND EXISTS (
               SELECT 1 FROM role_permissions rp JOIN permissions o ON o.id = rp.permission_id
               WHERE rp.role_id = r.id AND o.key LIKE '%.admin' AND o.key <> 'sync.admin')))
ON CONFLICT DO NOTHING;
