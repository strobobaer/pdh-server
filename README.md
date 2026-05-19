# PDH Server

PDH Server ist das Backend und die Web-Oberflaeche fuer den **PDH - Prozess Data Hub**. Das Projekt stellt eine modulare HTTP-API und Web-UI fuer betriebliche Daten bereit, unter anderem fuer Benutzer, Schichten, Lagerorte, Infrastruktur, Tickets, Stoerungen, IT, Checklisten, Anhaenge, Zeiterfassung, Wartung und Inventar.

## Inhalt

- [Funktionsumfang](#funktionsumfang)
- [Technik-Stack](#technik-stack)
- [Projektstruktur](#projektstruktur)
- [Voraussetzungen](#voraussetzungen)
- [Installation](#installation)
- [Konfiguration](#konfiguration)
- [Datenbank einrichten](#datenbank-einrichten)
- [Migration ausfuehren](#migration-ausfuehren)
- [Server starten](#server-starten)
- [API-Endpunkte](#api-endpunkte)
- [Web-UI](#web-ui)
- [Deployment mit systemd](#deployment-mit-systemd)
- [Entwicklung](#entwicklung)
- [Sicherheitshinweise](#sicherheitshinweise)
- [Bekannte Bremskloetze](#bekannte-bremskloetze)
- [Troubleshooting](#troubleshooting)

## Funktionsumfang

Aktuell registrierte Module:

- `users` - Benutzer, Login, Registrierung, Rollen
- `storage` - Lagerorte und Lagerstruktur
- `shifts` - Schichtmodelle und Schichtzuweisungen
- `infrastructure` - Bauwerke, Linien, Anlagen und Geraete
- `tickets` - Tickets und Aufgaben
- `faults` - Stoerungen mit optionalem Copilot
- `time` - Zeiterfassung
- `maintenance` - Wartung
- `it` - IT-Modul
- `inventory` - Inventar
- `attachments` - Anhaenge und Uploads
- `checklists` - Checklisten
- Web-UI unter `/`
- statische Uploads unter `/uploads/`

## Technik-Stack

- Go `1.22.2`
- PostgreSQL
- `github.com/go-chi/chi/v5` fuer Routing
- `github.com/jackc/pgx/v5` fuer PostgreSQL
- `github.com/golang-jwt/jwt/v5` fuer JWT-Authentifizierung
- `github.com/rs/zerolog` fuer Logging
- `github.com/spf13/viper` fuer Konfiguration
- `golang.org/x/crypto/bcrypt` fuer Passwort-Hashes
- Go HTML Templates fuer die Web-UI

## Projektstruktur

```text
.
├── cmd/server/                 # Einstiegspunkt des HTTP-Servers
├── internal/core/               # Kernmodule: users, shifts, storage, infrastructure
├── internal/modules/            # Fachmodule: faults, inventory, maintenance, tickets, timetracking, it, attachments, checklists
├── internal/web/                # Web-Handler
├── web/templates/               # Go HTML Templates
├── migrations/                  # SQL-Migrationen
├── pkg/config/                  # Konfiguration ueber .env, config.yaml und ENV
├── pkg/database/                # PostgreSQL-Verbindung
├── pkg/logger/                  # Logging
├── pkg/middleware/              # HTTP-Middleware, Auth, CORS
├── pkg/response/                # JSON/Error-Responses
├── .env.example                 # Beispielkonfiguration
├── go.mod
└── Makefile
```

## Voraussetzungen

Auf dem Zielsystem werden benoetigt:

- Linux-Server oder Entwicklungsmaschine
- Go `1.22.2` oder neuer
- PostgreSQL
- Git
- Make
- Optional: Ollama fuer lokalen Copilot-Betrieb

### Debian/Ubuntu installieren

```bash
sudo apt update
sudo apt install -y git make postgresql postgresql-contrib
```

Go pruefen:

```bash
go version
```

## Installation

Repository klonen:

```bash
git clone https://github.com/strobobaer/pdh-server.git
cd pdh-server
```

Abhaengigkeiten laden:

```bash
go mod tidy
```

Konfiguration vorbereiten:

```bash
cp .env.example .env
nano .env
```

## Konfiguration

Die Konfiguration wird in dieser Reihenfolge geladen:

1. `.env`
2. `config.yaml`, falls vorhanden
3. echte Umgebungsvariablen mit Prefix `PDH_`

Umgebungsvariablen haben die hoechste Prioritaet.

Wichtige Variablen:

```env
PDH_SERVER_HOST=0.0.0.0
PDH_SERVER_PORT=8090
PDH_SERVER_ENV=development

PDH_DATABASE_HOST=localhost
PDH_DATABASE_PORT=5432
PDH_DATABASE_USER=pdh
PDH_DATABASE_PASSWORD=SicheresPasswortAendern
PDH_DATABASE_NAME=pdh
PDH_DATABASE_SSLMODE=disable

PDH_AUTH_JWTSECRET=AENDERN-min32zeichen-openssl-rand-hex-32
PDH_AUTH_TOKENDURATION=24

PDH_COPILOT_BACKEND=ollama
PDH_COPILOT_OLLAMAURL=http://localhost:11434
PDH_COPILOT_MODEL=llama3.2
PDH_COPILOT_ANTHROPICKEY=
```

JWT-Secret erzeugen:

```bash
openssl rand -hex 32
```

Den ausgegebenen Wert in `.env` bei `PDH_AUTH_JWTSECRET` eintragen.

## Datenbank einrichten

PostgreSQL-Benutzer und Datenbank anlegen:

```bash
sudo -u postgres psql
```

In der PostgreSQL-Konsole:

```sql
CREATE USER pdh WITH PASSWORD 'SicheresPasswortAendern';
CREATE DATABASE pdh OWNER pdh;
\q
```

Verbindung testen:

```bash
psql "postgres://pdh:SicheresPasswortAendern@localhost:5432/pdh?sslmode=disable"
```

## Migration ausfuehren

Migrationen liegen im Ordner `migrations/`.

Beispiel fuer die Core-Migration:

```bash
psql "postgres://pdh:SicheresPasswortAendern@localhost:5432/pdh?sslmode=disable" \
  -f migrations/001_core_schema.up.sql
```

Falls weitere Migrationen vorhanden sind, diese in numerischer Reihenfolge ausfuehren.

Hinweis: Ein automatischer Migration-Runner ist derzeit noch nicht enthalten.

## Server starten

Mit Makefile:

```bash
make run
```

Oder direkt:

```bash
go run ./cmd/server/...
```

Standardadresse:

```text
http://localhost:8090
```

Healthcheck:

```bash
curl http://localhost:8090/health
```

Erwartete Antwort:

```json
{
  "service": "pdh",
  "status": "ok",
  "version": "0.8.0"
}
```

## API-Endpunkte

Basis-Pfad:

```text
/api/v1
```

Registrierte Routen:

```text
GET  /health

POST /api/v1/users/login
POST /api/v1/users/register
GET  /api/v1/users/
GET  /api/v1/users/{id}
PUT  /api/v1/users/{id}
DEL  /api/v1/users/{id}

/api/v1/storage
/api/v1/shifts
/api/v1/infrastructure
/api/v1/tickets
/api/v1/faults
/api/v1/time
/api/v1/maintenance
/api/v1/it
/api/v1/inventory
/api/v1/attachments
/api/v1/checklists
```

Geschuetzte Endpunkte erwarten einen Bearer-Token:

```bash
curl -H "Authorization: Bearer <TOKEN>" http://localhost:8090/api/v1/users/
```

## Web-UI

Die Web-UI wird unter `/` gemountet.

```text
http://localhost:8090/
```

Templates werden aus folgendem Pfad geladen:

```text
web/templates/base.gohtml
```

Uploads werden lokal im Ordner `uploads/` gespeichert und unter `/uploads/` ausgeliefert.

## Deployment mit systemd

Binary bauen:

```bash
make build
```

Oder direkt:

```bash
go build -o bin/pdh ./cmd/server/...
```

Beispiel-Service erstellen:

```bash
sudo nano /etc/systemd/system/pdh.service
```

Inhalt:

```ini
[Unit]
Description=PDH Server
After=network.target postgresql.service

[Service]
Type=simple
WorkingDirectory=/opt/pdh-server
ExecStart=/opt/pdh-server/bin/pdh
Restart=always
RestartSec=5
EnvironmentFile=/opt/pdh-server/.env
User=pdh
Group=pdh

[Install]
WantedBy=multi-user.target
```

Dienst aktivieren:

```bash
sudo systemctl daemon-reload
sudo systemctl enable pdh
sudo systemctl start pdh
sudo systemctl status pdh --no-pager -l
```

Logs anzeigen:

```bash
journalctl -u pdh -f
```

## Entwicklung

Formatieren:

```bash
gofmt -w .
```

Abhaengigkeiten aufraeumen:

```bash
make tidy
```

Bauen:

```bash
make build
```

Starten:

```bash
make run
```

Aenderungen pushen:

```bash
make push
```

## Sicherheitshinweise

Vor produktivem Einsatz pruefen:

1. `PDH_AUTH_JWTSECRET` muss zufaellig und mindestens 32 Zeichen lang sein.
2. `PDH_SERVER_ENV` sollte in Produktion auf `production` gesetzt werden.
3. CORS sollte nicht dauerhaft mit `*` betrieben werden.
4. Die oeffentliche Registrierung sollte fuer interne Installationen eingeschraenkt werden.
5. Datenbankpasswoerter duerfen nicht committed werden.
6. Fuer externe Datenbankverbindungen sollte SSL aktiviert werden.
7. Backups fuer PostgreSQL einrichten.
8. Der Ordner `uploads/` sollte nicht ins Repository committed werden.
9. Branch Protection fuer `main` ist empfohlen.

## Bekannte Bremskloetze

- Noch kein Dockerfile vorhanden.
- Noch keine `docker-compose.yml` vorhanden.
- Noch kein automatischer Migration-Runner vorhanden.
- CORS sollte fuer Produktion konfigurierbar gemacht werden.
- Registrierung ist aktuell als oeffentliche Route dokumentiert und sollte fuer interne Installationen geprueft werden.
- Upload-Speicherung erfolgt lokal im Dateisystem.

## Troubleshooting

### Fehler: Datenbank nicht erreichbar

Pruefen:

```bash
sudo systemctl status postgresql
psql "postgres://pdh:SicheresPasswortAendern@localhost:5432/pdh?sslmode=disable"
```

### Fehler: JWT ungueltig

Pruefen:

- Ist `PDH_AUTH_JWTSECRET` gesetzt?
- Wurde der Server nach Aenderung der `.env` neu gestartet?
- Wird der Header korrekt gesendet?

```text
Authorization: Bearer <TOKEN>
```

### Fehler: Port belegt

```bash
sudo ss -tulpn | grep 8090
```

Anderen Port setzen:

```env
PDH_SERVER_PORT=8091
```

### Fehler: Templates koennen nicht geladen werden

Pruefen, ob die Datei vorhanden ist:

```bash
ls -la web/templates/base.gohtml
```

Wenn der Dienst per systemd laeuft, muss `WorkingDirectory` auf das Repository-Verzeichnis zeigen.

### Fehler: Migration wurde schon ausgefuehrt

Einige Migrationen koennen `CREATE TABLE`-Statements ohne `IF NOT EXISTS` enthalten. Wenn Tabellen bereits existieren, muss die Datenbank bereinigt oder eine Folgemigration erstellt werden.

## Lizenz

Noch keine Lizenzdatei gefunden. Falls das Repository oeffentlich bleibt, sollte eine Lizenzdatei ergaenzt werden.
