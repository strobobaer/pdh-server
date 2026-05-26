# Lokaler Sync und Rollback

Diese Anleitung ist fuer den Server `pdh`, wenn der lokale Stand von GitHub `main` abweicht oder lokale Aenderungen einen `git pull` blockieren.

## 1. Lokalen Zustand pruefen

```bash
cd ~/pdh
git status
git log --oneline -5
git rev-parse --short HEAD
```

Wenn `git status` lokale Aenderungen zeigt, nicht mit `git reset --hard` fortfahren, bevor die Aenderungen gesichert wurden.

## 2. Lokale Aenderungen sichern

```bash
git stash push -u -m "local-before-sync"
```

Stash pruefen:

```bash
git stash list
git stash show -p stash@{0}
```

## 3. GitHub-Stand holen

```bash
git fetch origin
git log --oneline --left-right --graph HEAD...origin/main
git diff --stat HEAD..origin/main
```

## 4. Aktualisieren

Wenn keine lokalen Commits vorhanden sind:

```bash
git pull --ff-only
```

Wenn `--ff-only` scheitert:

```bash
git merge origin/main
```

## 5. Build pruefen

```bash
go mod tidy
gofmt -w .
go test ./...
go build ./cmd/server/...
```

## 6. Migrationen pruefen

Neue Migrationen in numerischer Reihenfolge ausfuehren. Beispiel:

```bash
psql "postgres://pdh:SicheresPasswortAendern@localhost:5432/pdh?sslmode=disable" \
  -f migrations/011_migration_hardening.up.sql

psql "postgres://pdh:SicheresPasswortAendern@localhost:5432/pdh?sslmode=disable" \
  -f migrations/012_record_media_assignments_history.up.sql

psql "postgres://pdh:SicheresPasswortAendern@localhost:5432/pdh?sslmode=disable" \
  -f migrations/013_it_asset_infrastructure.up.sql
```

Passwort aus `.env` verwenden.

## 7. Dienst neu starten

```bash
sudo systemctl restart pdh
sudo systemctl status pdh --no-pager -l
curl http://localhost:8090/health
```

## 8. Funktionstest

Im Browser pruefen:

```text
/tickets
/faults
/maintenance
/infrastructure
```

Testfaelle:

1. Neues Ticket mit Infrastruktur anlegen.
2. Neue Stoerung mit Infrastruktur anlegen.
3. Wartungsplan mit Infrastruktur anlegen.
4. Infrastruktur-Picker oeffnen, suchen, auswaehlen, leeren.
5. Standardbuttons pruefen: Neu, Aktualisieren, Bearbeiten, Kopieren, Loeschen/Loeschvormerkung, Aktiv/Inaktiv, Fertig, Abbrechen.

## 9. Rollback auf vorherigen lokalen Stand

Wenn der neue Stand nicht startet:

```bash
git log --oneline -10
```

Dann vorherigen Commit auswaehlen und temporaer zuruecksetzen:

```bash
git checkout <commit-sha>
go build ./cmd/server/...
sudo systemctl restart pdh
```

Zurueck auf `main`:

```bash
git checkout main
git pull --ff-only
```

## 10. Stash wieder einspielen

Nur wenn die lokalen Aenderungen wirklich gebraucht werden:

```bash
git stash pop
```

Bei Konflikten:

```bash
git status
nano <konflikt-datei>
gofmt -w .
go build ./cmd/server/...
```
