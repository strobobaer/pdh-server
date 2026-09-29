-- 070_user_master_and_broker.up.sql
--
-- Benutzerstamm: fehlende betriebliche Stammdaten, Broker-Funktion und
-- private Daten.
--   * Betriebliche Daten liegen in users (fuer alle mit Zugriff auf den
--     Benutzerstamm sichtbar).
--   * Private Daten liegen getrennt in user_private_data und sind nur mit
--     der Berechtigung users.private_data (Standard: Admin, Manager) bzw.
--     fuer die Person selbst sichtbar.
--   * Broker: nicht zugewiesene Tickets/Stoerungen aus dem globalen
--     Dashboard gehen an alle Broker des jeweiligen Typs.

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS personnel_no   VARCHAR(50)  NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS job_title      VARCHAR(100) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS cost_center_id UUID REFERENCES cost_centers(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS work_location  VARCHAR(150) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS phone_internal VARCHAR(30)  NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS phone_mobile   VARCHAR(50)  NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS entry_date     DATE,
    ADD COLUMN IF NOT EXISTS exit_date      DATE,
    ADD COLUMN IF NOT EXISTS language       VARCHAR(10)  NOT NULL DEFAULT 'de',
    ADD COLUMN IF NOT EXISTS master_notes   TEXT         NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS broker_tickets BOOLEAN      NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS broker_faults  BOOLEAN      NOT NULL DEFAULT false;

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_personnel_no ON users(personnel_no) WHERE personnel_no <> '';
CREATE INDEX IF NOT EXISTS idx_users_broker ON users(broker_tickets, broker_faults) WHERE broker_tickets OR broker_faults;

CREATE TABLE IF NOT EXISTS user_private_data (
    user_id                    UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    birth_date                 DATE,
    street                     VARCHAR(200) NOT NULL DEFAULT '',
    postal_code                VARCHAR(20)  NOT NULL DEFAULT '',
    city                       VARCHAR(100) NOT NULL DEFAULT '',
    country                    VARCHAR(100) NOT NULL DEFAULT '',
    private_phone              VARCHAR(50)  NOT NULL DEFAULT '',
    private_mobile             VARCHAR(50)  NOT NULL DEFAULT '',
    private_email              VARCHAR(150) NOT NULL DEFAULT '',
    emergency_contact_name     VARCHAR(150) NOT NULL DEFAULT '',
    emergency_contact_relation VARCHAR(100) NOT NULL DEFAULT '',
    emergency_contact_phone    VARCHAR(50)  NOT NULL DEFAULT '',
    notes                      TEXT         NOT NULL DEFAULT '',
    updated_by                 UUID REFERENCES users(id) ON DELETE SET NULL,
    updated_at                 TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

INSERT INTO permissions (key, label, category) VALUES
    ('users.private_data', 'Private Mitarbeiterdaten ansehen & pflegen', 'System')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.key IN ('admin', 'manager') AND p.key = 'users.private_data'
ON CONFLICT DO NOTHING;
