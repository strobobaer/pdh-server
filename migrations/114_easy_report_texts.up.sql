-- 114_easy_report_texts.up.sql
-- Vorformulierte Meldetexte fuer den Easy-Mode (internal/web/easy_texts.go).
--
-- easy_report_texts  zentraler Katalog im Infrastruktur-Stamm (deutsch)
--                    report_type: fuer Stoerung, Ticket oder beide
--                    kind: belegt „elektrisch/mechanisch“ vor ('' = fragen)
-- infra_easy_texts   Zuordnung zu Anlagen; gilt auch fuer alle Unteranlagen

CREATE TABLE IF NOT EXISTS easy_report_texts (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    text        VARCHAR(200) NOT NULL,
    report_type VARCHAR(10)  NOT NULL DEFAULT 'both' CHECK (report_type IN ('both', 'fault', 'ticket')),
    kind        VARCHAR(12)  NOT NULL DEFAULT '' CHECK (kind IN ('', 'electrical', 'mechanical')),
    created_by  UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS infra_easy_texts (
    infrastructure_id UUID NOT NULL REFERENCES infrastructure(id) ON DELETE CASCADE,
    text_id           UUID NOT NULL REFERENCES easy_report_texts(id) ON DELETE CASCADE,
    PRIMARY KEY (infrastructure_id, text_id)
);
CREATE INDEX IF NOT EXISTS idx_infra_easy_texts_text ON infra_easy_texts(text_id);
