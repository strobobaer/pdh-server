-- PDH-App Paket 3 (Arbeiten ohne Verbindung): Schritte genau einmal und Kennungs-Aliase.
--
-- app_steps: jeder schreibende Schritt aus der App trägt die Kennung, die das Telefon
--   vergeben hat (X-PDH-Once, UUIDv7), hier ein. Wiederholt das Telefon den Schritt
--   (Netz weg, Antwort verloren), erkennt der Server ihn daran und führt ihn nicht
--   noch einmal aus. result_type/result_id merken sich, was der Schritt angelegt hat,
--   damit die Wiederholung dasselbe Ergebnis erhält. Aufbewahrung: 90 Tage – so lange
--   darf ein Telefon offline bleiben, ohne dass ein Schritt doppelt ausgeführt wird.
--   Aufgeräumt wird beim Schreiben (gelegentlich), nicht per Zeitplan.
-- app_id_aliases: das Telefon legt offline Datensätze mit eigener Kennung (UUIDv7) an;
--   wird eine Meldung an einen bestehenden Datensatz angehängt, verweist die Kennung
--   vom Telefon hier auf den Datensatz, der sie tatsächlich aufgenommen hat. Einträge
--   bleiben, solange der Datensatz besteht (kein Aufräumen).

CREATE TABLE IF NOT EXISTS app_steps (
    id          UUID PRIMARY KEY DEFAULT pdh_uuid_v7(), -- Kennung des Schritts vom Telefon (X-PDH-Once)
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device_id   UUID REFERENCES app_devices(id) ON DELETE SET NULL,
    path        TEXT NOT NULL,
    result_type TEXT,
    result_id   UUID,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS app_steps_created ON app_steps (created_at);

CREATE TABLE IF NOT EXISTS app_id_aliases (
    id          UUID PRIMARY KEY DEFAULT pdh_uuid_v7(), -- Kennung vom Telefon
    target_type TEXT NOT NULL,
    target_id   UUID NOT NULL,
    created_by  UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS app_id_aliases_target ON app_id_aliases (target_type, target_id);
