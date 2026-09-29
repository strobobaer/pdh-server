-- 071_user_qualifications.up.sql
--
-- Nachweise je Mitarbeiter: Fuehrerschein (Klassen), externe
-- Qualifikationen/Zertifikate (z. B. Elektrofachkraft, Staplerschein,
-- Schweisserpruefung) und Lehrgaenge/Schulungen - jeweils mit Aussteller,
-- Nummer, Datum und Ablaufdatum (Warnung vor Ablauf).
-- Die Nummer (z. B. Fuehrerscheinnummer) ist nur fuer Berechtigte auf
-- private Daten sichtbar.

CREATE TABLE IF NOT EXISTS user_qualifications (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind        VARCHAR(20)  NOT NULL CHECK (kind IN ('license', 'qualification', 'course')),
    title       VARCHAR(200) NOT NULL,
    classes     VARCHAR(100) NOT NULL DEFAULT '',   -- Fuehrerscheinklassen, Stufe, Umfang
    issuer      VARCHAR(200) NOT NULL DEFAULT '',   -- Behoerde, Pruefstelle, Schulungsanbieter
    number      VARCHAR(100) NOT NULL DEFAULT '',   -- Nachweis-/Fuehrerscheinnummer (vertraulich)
    issued_on   DATE,
    valid_until DATE,
    hours       NUMERIC(6,1),                       -- Lehrgangsdauer in Stunden
    notes       VARCHAR(1000) NOT NULL DEFAULT '',
    created_by  UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_user_qualifications_user ON user_qualifications(user_id, kind);
CREATE INDEX IF NOT EXISTS idx_user_qualifications_valid ON user_qualifications(valid_until) WHERE valid_until IS NOT NULL;
