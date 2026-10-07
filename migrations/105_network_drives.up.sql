-- 105_network_drives.up.sql
-- Netzlaufwerke (SMB/CIFS, NFS oder bereits eingebundener Pfad), die PDH
-- ueber den Update-Agenten unter /mnt/pdh/<key> einbindet und verwendet als:
--   use_data         globaler Datenspeicher: Standardablage, relative
--                    Import-/Export-Pfade, Sicherungen und Dokumente,
--                    sofern dafuer kein eigenes Laufwerk gewaehlt ist
--   use_import_export Quelle/Ziel fuer Import- und Export-Verbindungen
--   use_backup       Ablage der Datensicherungen
--   use_documents    Dokumentenablage: Anhaenge werden nach Vorgang sortiert abgelegt
-- Passwoerter liegen verschluesselt (AES-GCM, wie Partner-Zugangsdaten).

CREATE TABLE IF NOT EXISTS network_drives (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    key               VARCHAR(32)  NOT NULL UNIQUE CHECK (key ~ '^[a-z0-9][a-z0-9-]{0,31}$'),
    name              VARCHAR(100) NOT NULL,
    kind              VARCHAR(10)  NOT NULL CHECK (kind IN ('smb', 'nfs', 'local')),
    source            VARCHAR(500) NOT NULL,
    username          VARCHAR(256) NOT NULL DEFAULT '',
    password_enc      TEXT NOT NULL DEFAULT '',
    domain            VARCHAR(100) NOT NULL DEFAULT '',
    options           VARCHAR(300) NOT NULL DEFAULT '',
    read_only         BOOLEAN NOT NULL DEFAULT false,
    auto_mount        BOOLEAN NOT NULL DEFAULT true,
    use_data          BOOLEAN NOT NULL DEFAULT false,
    use_import_export BOOLEAN NOT NULL DEFAULT false,
    use_backup        BOOLEAN NOT NULL DEFAULT false,
    use_documents     BOOLEAN NOT NULL DEFAULT false,
    last_status       VARCHAR(20) NOT NULL DEFAULT '',
    last_error        TEXT NOT NULL DEFAULT '',
    last_checked_at   TIMESTAMPTZ,
    created_by        UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- je Aufgabe hoechstens ein Laufwerk
CREATE UNIQUE INDEX IF NOT EXISTS idx_network_drives_data ON network_drives (use_data) WHERE use_data;
CREATE UNIQUE INDEX IF NOT EXISTS idx_network_drives_backup ON network_drives (use_backup) WHERE use_backup;
CREATE UNIQUE INDEX IF NOT EXISTS idx_network_drives_documents ON network_drives (use_documents) WHERE use_documents;

-- bereits in der Dokumentenablage abgelegte Anhaenge
ALTER TABLE attachments ADD COLUMN IF NOT EXISTS drive_copied_at TIMESTAMPTZ;
