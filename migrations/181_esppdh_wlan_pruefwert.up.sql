-- ESPPDH (0.82.0), Nachträge aus der Schlussprüfung.

-- 1. WLANs der Seite „Netzwerk“ stehen ESPPDH-Geräten nur zur Wahl, wenn sie dafür
--    freigegeben sind (Haken „Für ESPPDH-Geräte nutzbar“; setzen darf nur, wer die Seite
--    pflegt – system.network). Das WLAN-Kennwort steht lesbar in der Firmware, und die darf
--    jeder mit esppdh.edit herunterladen. Vorgabe aus: auch vorhandene WLANs sind erst nach
--    dem Setzen des Hakens wählbar.
ALTER TABLE net_wlans ADD COLUMN IF NOT EXISTS esppdh_ok BOOLEAN NOT NULL DEFAULT false;

-- 2. Prüfwert des MQTT-Kennworts je Gerät: SHA-256 als Hex-Text. Der eingebaute Broker prüft
--    die Anmeldung damit statt mit dem entschlüsselten Kennwort – so überstehen die Zugänge
--    einen Wechsel des Schlüssels (PDH_CREDENTIALS_KEY bzw. Anmelde-Schlüssel), und der Broker
--    hält keine Kennwörter im Klartext. Ein einfacher Hash genügt: Das Kennwort sind 24 Byte
--    (192 Bit) Zufall, Durchprobieren ist aussichtslos. Leer = noch nicht gesetzt (Geräte von
--    vor dieser Migration); der Abgleich mit dem Broker trägt ihn nach.
ALTER TABLE esppdh_devices ADD COLUMN IF NOT EXISTS mqtt_pw_sha256 TEXT NOT NULL DEFAULT '';
