-- 134_management_systems.up.sql
-- Managementsysteme: gelenkte Dokumente (ISO 9001/14001/45001/50001/27001)
--   Handbücher, Prozessbeschreibungen, Verfahrens- und Arbeitsanweisungen,
--   Formblätter, Leitlinien – mit Nummer, Versionen, Prüfung und Freigabe
--   (Vier-Augen-Prinzip), Überprüfungsfrist, Kenntnisnahme und mitgeltenden
--   Dokumenten. Dateien hängen als Anhang (ref_type 'ms_document') daran.
-- Rechte:
--   ms.view    – freigegebene Dokumente lesen, Kenntnisnahme bestätigen
--   ms.edit    – Dokumente anlegen, Entwürfe bearbeiten, zur Prüfung geben
--   ms.approve – prüfen und freigeben (nicht die eigene Fassung), zurückziehen
--   ms.manage  – Managementsysteme pflegen

INSERT INTO permissions (key, label, category) VALUES
    ('ms.view',    'Managementsysteme: Dokumente lesen und Kenntnisnahme bestätigen', 'Managementsysteme'),
    ('ms.edit',    'Managementsysteme: Dokumente anlegen und Entwürfe bearbeiten',    'Managementsysteme'),
    ('ms.approve', 'Managementsysteme: Dokumente prüfen, freigeben und zurückziehen', 'Managementsysteme'),
    ('ms.manage',  'Managementsysteme: Systeme verwalten',                           'Managementsysteme')
ON CONFLICT (key) DO NOTHING;

-- lesen: alle Rollen; bearbeiten/freigeben/verwalten: Administratoren und Manager
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p WHERE p.key = 'ms.view'
ON CONFLICT DO NOTHING;
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.key IN ('admin', 'manager') AND p.key IN ('ms.edit', 'ms.approve', 'ms.manage')
ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS ms_systems (
    id         UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    code       VARCHAR(10)  NOT NULL UNIQUE, -- Präfix der Dokumentnummer (QM, UM …)
    name       VARCHAR(120) NOT NULL,
    norm       VARCHAR(60)  NOT NULL DEFAULT '',
    color      VARCHAR(7)   NOT NULL DEFAULT '#3B82F6',
    sort_order INT          NOT NULL DEFAULT 0,
    active     BOOLEAN      NOT NULL DEFAULT true
);
INSERT INTO ms_systems (code, name, norm, color, sort_order) VALUES
    ('IMS', 'Integriertes Managementsystem', '',              '#64748B', 0),
    ('QM',  'Qualitätsmanagement',           'ISO 9001',      '#3B82F6', 1),
    ('UM',  'Umweltmanagement',              'ISO 14001',     '#10B981', 2),
    ('AS',  'Arbeitsschutzmanagement',       'ISO 45001',     '#F59E0B', 3),
    ('EM',  'Energiemanagement',             'ISO 50001',     '#8B5CF6', 4),
    ('IS',  'Informationssicherheit',        'ISO/IEC 27001', '#EF4444', 5)
ON CONFLICT (code) DO NOTHING;

CREATE TABLE IF NOT EXISTS ms_documents (
    id                  UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    doc_no              VARCHAR(40)  NOT NULL UNIQUE,
    doc_type            VARCHAR(20)  NOT NULL CHECK (doc_type IN ('handbook', 'process', 'procedure', 'instruction', 'form', 'policy', 'other')),
    system_id           UUID NOT NULL REFERENCES ms_systems(id),
    title               VARCHAR(200) NOT NULL,
    owner_id            UUID REFERENCES users(id) ON DELETE SET NULL,      -- verantwortlich (Prozesseigner)
    department_id       UUID REFERENCES departments(id) ON DELETE SET NULL,
    review_months       INT NOT NULL DEFAULT 12 CHECK (review_months BETWEEN 0 AND 120), -- 0 = keine Überprüfung
    next_review         DATE,
    read_scope          VARCHAR(12) NOT NULL DEFAULT 'none' CHECK (read_scope IN ('none', 'all', 'department')),
    withdrawn_at        TIMESTAMPTZ,
    released_version_id UUID, -- aktuell gültige Fassung (ms_document_versions)
    created_by          UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_ms_documents_type ON ms_documents (doc_type, system_id);

CREATE TABLE IF NOT EXISTS ms_document_versions (
    id           UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    document_id  UUID NOT NULL REFERENCES ms_documents(id) ON DELETE CASCADE,
    version      INT  NOT NULL,
    status       VARCHAR(12) NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'review', 'released', 'superseded', 'rejected')),
    title        VARCHAR(200) NOT NULL,
    content      TEXT NOT NULL DEFAULT '',
    norm_refs    VARCHAR(300) NOT NULL DEFAULT '', -- Normkapitel, z. B. „ISO 9001: 7.5, 8.5“
    change_note  TEXT NOT NULL DEFAULT '',
    author_id    UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    submitted_at TIMESTAMPTZ,
    reviewed_by  UUID REFERENCES users(id) ON DELETE SET NULL,
    review_note  TEXT NOT NULL DEFAULT '',
    released_at  TIMESTAMPTZ,
    UNIQUE (document_id, version)
);
-- höchstens eine offene Fassung (Entwurf oder in Prüfung) je Dokument
CREATE UNIQUE INDEX IF NOT EXISTS idx_ms_versions_open ON ms_document_versions (document_id) WHERE status IN ('draft', 'review');

ALTER TABLE ms_documents DROP CONSTRAINT IF EXISTS ms_documents_released_fk;
ALTER TABLE ms_documents ADD CONSTRAINT ms_documents_released_fk FOREIGN KEY (released_version_id) REFERENCES ms_document_versions(id) ON DELETE SET NULL;

CREATE TABLE IF NOT EXISTS ms_document_relations (
    document_id UUID NOT NULL REFERENCES ms_documents(id) ON DELETE CASCADE,
    related_id  UUID NOT NULL REFERENCES ms_documents(id) ON DELETE CASCADE,
    PRIMARY KEY (document_id, related_id),
    CHECK (document_id <> related_id)
);

CREATE TABLE IF NOT EXISTS ms_read_receipts (
    version_id UUID NOT NULL REFERENCES ms_document_versions(id) ON DELETE CASCADE,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    read_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (version_id, user_id)
);
