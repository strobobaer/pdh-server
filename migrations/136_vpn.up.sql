-- 136_vpn.up.sql
-- WireGuard-VPN ins Maschinennetz (internal/web/vpn.go, Dienst „wireguard“ in compose.yaml).
--
-- PDH ist Hub (Schnittstelle wg0):
--   * user    – Gerät einer Person (Laptop, Handy), wählt sich bei PDH ein
--   * gateway – Router im Maschinennetz (Teltonika, mGuard …), wählt sich bei PDH ein
--               und gibt seine Netze (routes) bekannt
-- PDH ist Client (je Tunnel eine eigene Schnittstelle):
--   * outbound – PDH verbindet sich zu einem entfernten WireGuard-Server (endpoint,
--               remote_public_key); die Netze dahinter (routes) erreichen eingewählte
--               Geräte über NAT.
-- Eingewählte Geräte dürfen nur in die Netze von Gateways und ausgehenden Tunneln,
-- nicht zueinander. Schlüssel liegen verschlüsselt in der Datenbank; der Server-
-- Schlüssel in app_settings (vpn.server_key).
-- Rechte:
--   vpn.use    – eigene Zugänge anlegen, Konfiguration laden, sperren
--   vpn.manage – Einstellungen, Gateways, ausgehende Tunnel, alle Zugänge, Protokoll

INSERT INTO permissions (key, label, category) VALUES
    ('vpn.use',    'VPN: eigene Zugänge ins Maschinennetz anlegen und nutzen',             'VPN'),
    ('vpn.manage', 'VPN: Server, Gateways, ausgehende Tunnel und alle Zugänge verwalten', 'VPN')
ON CONFLICT (key) DO NOTHING;

-- Zugang zum Maschinennetz ist heikel: anfangs nur Administratoren und Anlagenverantwortliche
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE (r.key IN ('admin', 'asset_manager') AND p.key = 'vpn.use') OR (r.key = 'admin' AND p.key = 'vpn.manage')
ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS vpn_peers (
    id                UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    kind              VARCHAR(10) NOT NULL CHECK (kind IN ('user', 'gateway', 'outbound')),
    name              VARCHAR(80) NOT NULL,
    user_id           UUID REFERENCES users(id) ON DELETE CASCADE,    -- nur user
    address           VARCHAR(50) NOT NULL,                           -- user/gateway: Adresse im VPN-Netz; outbound: eigene Tunneladresse (CIDR)
    public_key        VARCHAR(60) NOT NULL,                           -- eigener öffentlicher Schlüssel des Eintrags
    private_key_enc   TEXT NOT NULL,                                  -- verschlüsselt (für Konfiguration zum Herunterladen bzw. outbound)
    psk_enc           TEXT NOT NULL DEFAULT '',                       -- verschlüsselter Pre-Shared-Key
    routes            TEXT NOT NULL DEFAULT '',                       -- gateway/outbound: Netze dahinter, kommagetrennt
    endpoint          VARCHAR(255) NOT NULL DEFAULT '',               -- nur outbound: host:port des entfernten Servers
    remote_public_key VARCHAR(60) NOT NULL DEFAULT '',                -- nur outbound
    created_by        UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at        TIMESTAMPTZ,
    revoked_by        UUID REFERENCES users(id) ON DELETE SET NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_vpn_peers_address ON vpn_peers(address) WHERE revoked_at IS NULL AND kind <> 'outbound';
CREATE UNIQUE INDEX IF NOT EXISTS idx_vpn_peers_key ON vpn_peers(public_key) WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vpn_peers_user ON vpn_peers(user_id) WHERE user_id IS NOT NULL;

-- Protokoll: wer hat wann welchen Zugang angelegt, geladen, gesperrt …
CREATE TABLE IF NOT EXISTS vpn_events (
    id        UUID PRIMARY KEY DEFAULT pdh_uuid_v7(),
    at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    user_id   UUID REFERENCES users(id) ON DELETE SET NULL,
    peer_id   UUID REFERENCES vpn_peers(id) ON DELETE SET NULL,
    peer_name VARCHAR(80) NOT NULL DEFAULT '',
    action    VARCHAR(20) NOT NULL,
    note      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_vpn_events_at ON vpn_events(at DESC);
