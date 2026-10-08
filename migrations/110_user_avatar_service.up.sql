-- Profilbilder (avatar.go): Pfad unter uploads/avatars, quadratisch 256 px (JPEG).
ALTER TABLE users ADD COLUMN IF NOT EXISTS avatar_path TEXT;

-- Systemmeldungen erscheinen im Chat als „Service“ mit dem PDH-Logo
-- (vorher „PDH System“); Benutzer bleibt inaktiv und kann sich nie anmelden.
UPDATE users SET first_name = 'Service', last_name = ''
 WHERE id = '00000000-0000-4000-8000-00000000d0d1';
