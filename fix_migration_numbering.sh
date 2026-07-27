#!/usr/bin/env bash
# fix_migration_numbering.sh
#
# Behebt vier doppelt vergebene Migrations-Versionsnummern (004, 011, 012, 013).
# Der Runner (pkg/database/migrations.go) nimmt die Zahl vor dem ersten "_"
# als Primary Key in schema_migrations -> zwei Dateien mit derselben Nummer
# lassen eine FRISCHE Datenbank beim Start mit
#   "duplicate key value violates unique constraint schema_migrations_pkey"
# abbrechen. Auf einer bereits laufenden DB (wie aktuell auf dem Server)
# passiert nichts, weil die Nummern dort schon als erledigt eingetragen sind.
#
# Alle betroffenen SQL-Dateien sind idempotent (IF NOT EXISTS etc.), daher
# ist die Umbenennung ohne Risiko für die bestehende Produktions-DB.
#
# Ausführen im Wurzelverzeichnis deines lokalen pdh-server Checkouts:
#   bash fix_migration_numbering.sh

set -e

if [ ! -d migrations ] || [ ! -f go.mod ]; then
  echo "Fehler: bitte im Wurzelverzeichnis von pdh-server ausführen (migrations/ und go.mod erwartet)."
  exit 1
fi

echo "Benenne kollidierende Migrationsdateien um..."

git mv migrations/004_timetracking.up.sql migrations/040_timetracking.up.sql
git mv migrations/011_nextcloud_user_sync.up.sql migrations/041_nextcloud_user_sync.up.sql
git mv migrations/012_record_media_assignments_history.up.sql migrations/042_record_media_assignments_history.up.sql
git mv migrations/013_it_asset_infrastructure.up.sql migrations/043_it_asset_infrastructure.up.sql

sed -i '1s/.*/-- 040_timetracking.up.sql/' migrations/040_timetracking.up.sql
sed -i '1s/.*/-- 041_nextcloud_user_sync.up.sql/' migrations/041_nextcloud_user_sync.up.sql
sed -i '1s/.*/-- 042_record_media_assignments_history.up.sql/' migrations/042_record_media_assignments_history.up.sql
sed -i '1s/.*/-- 043_it_asset_infrastructure.up.sql/' migrations/043_it_asset_infrastructure.up.sql

echo
echo "Fertig. Doppelte Versionsnummern jetzt pruefen (sollte leer sein):"
ls migrations/ | sed 's/_.*//' | sort | uniq -d

echo
echo "Naechste Schritte:"
echo "  git add -A"
echo "  git commit -m 'Migrationen: doppelte Versionsnummern 004/011/012/013 behoben'"
echo "  make deploy && make push"
