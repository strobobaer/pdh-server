# Ausfallschutz Teil 6: Datei-Spiegelung und Nextcloud im Verbund

Stand: 2026-11-03 · Status: Entwurf zur Durchsicht ·
Erweitert: `2026-10-24-ausfallschutz-design.md` (Abschnitte 7, 9, 16) ·
Baut auf: `2026-10-30-verbund-update-design.md` (PR #14) ·
Version: nächste Korrekturversion nach dem Merge von PR #14

## 1. Ziel und Umfang

Datenbank, Adresse und Tunnel wandern beim Tausch schon mit, die Dateien nicht.
Nach einem Tausch fehlen auf dem anderen Server neue Anhänge, und Nextcloud läuft
nur auf einem Server.

Neu:

1. **Datei-Spiegelung:** Der aktive Server überträgt Dateien fast in Echtzeit auf
   den passiven. Die Richtung folgt der Rolle.
2. **Nextcloud im Verbund:** Nextcloud ist auf beiden Servern startbereit, aber
   nur auf dem aktiven arbeitet sie. Für Clients bleibt es derselbe Server unter
   derselben Adresse.
3. **Sichtbarkeit:** Der Reiter Ausfallschutz zeigt den Stand, Fehler lösen einen
   Alarm aus, und ein geplanter Tausch wartet auf einen letzten Abgleich.

**Erfolg heißt:** Nach einem geplanten Tausch fehlt keine Datei. Nach einem harten
Ausfall fehlen höchstens die letzten Sekunden bis zur letzten Minute, und der
Reiter nennt die betroffenen Nextcloud-Dateien.

**Nicht im Umfang:**

- n8n, pgAdmin und code-server (eigener Schritt danach, sie brauchen eigene
  Regeln für das konsistente Kopieren).
- Samba (Teil 7).
- Änderungen an der Sicherung aufs Netzlaufwerk. Sie läuft unverändert weiter und
  bleibt die einzige Sicherung gegen versehentliches Löschen über die
  Papierkorb-Frist hinaus.

**Korrektur der Gesamt-Spec:** Abschnitt 16 sagt „Nextcloud selbst ist nicht Teil
der Umschaltung“. Das gilt nicht mehr. Abschnitt 9 sieht SSH vor; Teil 6 nutzt den
Verbund-Kanal (Abschnitt 4).

### Verworfene Wege

| Weg | Warum nicht |
|---|---|
| Zwei eigenständige Nextcloud-Instanzen | Freigaben, Kalender, Kontakte, Versionen und Datei-IDs liegen in der Datenbank und liefen auseinander; Clients sähen einen fremden Server |
| Nextcloud auf dem Zeugen | Der Zeuge wäre eine neue einzelne Schwachstelle, seine Stimme würde unzuverlässig, und er ist optional |
| GlusterFS oder DRBD | Kein Dateiverlust, aber spürbar mehr Betriebsaufwand; Gluster bremst bei vielen kleinen Dateien, und eine klemmende Spiegelung legt Nextcloud lahm |
| Gemeinsamer Speicher (NAS, S3) | Der Speicher wäre die neue einzelne Schwachstelle |

Der bewusste Preis: Fällt der aktive Server im falschen Moment aus, kennt die
Datenbank eventuell eine Datei, die noch nicht übertragen ist (Abschnitt 8).

## 2. Begriffe

- **Quelle:** Ein Eintrag der Spiegelung mit festem Namen (Label) und Pfad, zum
  Beispiel `uploads`.
- **Sofort-Quelle:** Quelle, deren Änderungen per inotify gleich übertragen werden.
- **Abgleich:** Vollständiger rsync-Lauf über alle Quellen.
- **Erstabgleich:** Erster vollständiger Abgleich, solange der Empfänger noch nie
  vollständig war.
- **Papierkorb:** Ablage gelöschter und überschriebener Dateien auf dem Empfänger.

## 3. Inhalt der Spiegelung

| Label | Pfad | Sofort | Besonderheit |
|---|---|---|---|
| `uploads` | `<repo>/uploads` | ja | |
| `chat_files` | `<repo>/chat_files` | ja | |
| `etc-pdh` | `/etc/pdh` | nein | ohne `/ha/`; enthält `ha-hooks` |
| `nextcloud-data` | Datenordner | ja | ohne `appdata_*/preview/`, `*.part`, `nextcloud.log` |
| `nextcloud-app` | `/var/www/nextcloud` | nein | enthält `config.php`, ohne den Datenordner, falls er darin liegt |
| `nginx` | `/etc/nginx` | nein | danach `nginx -t` und Neuladen, nur bei Änderung |
| `ssl-pdh-nextcloud` | `/etc/ssl/pdh-nextcloud` | nein | |
| `letsencrypt` | `/etc/letsencrypt` | nein | |

Es gelten nur Quellen, die es gibt. Die Nextcloud-Einträge entfallen, wenn
„Nextcloud mitnehmen“ aus ist.

**Bewusst nicht gespiegelt:**

- **`.env`:** Enthält Serverbezogenes wie `PDH_UPDATE_BRANCH`; ein falscher Wert
  kann einen Server lahmlegen. Der Reiter vergleicht die Stände und warnt bei
  Abweichungen, außer bei einer festen Liste serverbezogener Schlüssel. Der Heartbeat
  trägt dazu wie bei den Skripten eine Prüfsumme als optionales Feld.
- **cloudflared:** Das Token verteilt Teil 5 bereits.
- **`/etc/pdh/ha/`:** Gehört zum Rechner und ist auf dem Empfänger immer gesperrt.

**Rsync-Optionen:** `-a --numeric-ids --backup --backup-dir=…`, dazu `--delete` nur nach der
Regel „Löschen nur mit Beleg“ aus Abschnitt 5; Zwischenname für unfertige Dateien
(`--delay-updates` und `--partial-dir=.pdh-teil`).
Anders als aufs Netzlaufwerk werden Besitzer und Rechte mitkopiert. Die Einrichtung
prüft, dass `www-data` und `pdh` auf beiden Servern dieselbe Kennung haben.

**Papierkorb:** `<Elternordner des Ziels>/.pdh-papierkorb/<JJJJ-MM-TT>/<Label>/`,
also auf demselben Dateisystem wie das Ziel und außerhalb des Nextcloud-Datenordners.
Standard 14 Tage.

## 4. Übertragung

**Ablauf:** rsync läuft durch den verschlüsselten Verbund-Kanal auf Port 7480:
`rsync -e "pdh-ha mirror-pipe <Knoten>"`. Der Befehl öffnet mit dem Zertifikat des
Knotens eine eigene TLS-Verbindung zu einer neuen Route am Partner. Der Partner
startet `rsync --server` und verbindet ihn mit dieser Verbindung.

**Empfänger, in dieser Reihenfolge:**

1. Zertifikat muss zu einem Mitglied des Verbunds gehören und darf nicht das des
   Zeugen sein.
2. Der eigene Knoten muss passiv sein.
3. Die mitgesendete Amtszeit des Absenders muss mindestens so hoch sein wie die
   eigene (Abschnitt 8).
4. Das Label muss in der eigenen Liste stehen. Der Pfad kommt nur aus dieser Liste,
   nie vom Absender. Die Optionen für `rsync --server` prüft der Empfänger gegen eine
   feste Liste; Zielpfad und Papierkorb setzt er selbst.

Den Datenordner von Nextcloud legt die Einrichtung in den Verbund-Einstellungen fest,
er wird nicht aus der Nachricht übernommen.

**Heartbeat bleibt frei:** Dateiverkehr läuft nur über eigene Verbindungen, höchstens
eine je Quelle gleichzeitig. Die einstellbare Bandbreitengrenze wird als `--bwlimit`
gesetzt. Das Labor prüft den Heartbeat unter Dateilast (Abschnitt 12).

## 5. Ablauf

**Sofort-Quellen:** Ein Worker beobachtet die Ordner rekursiv per inotify (`fsnotify` ist
schon im Projekt) und sammelt Änderungen 2 Sekunden lang. Dann überträgt er nur die
betroffenen Pfade (`--files-from`), und nur innerhalb der Quelle.

**Abgleich:** Jede Minute (einstellbar) läuft ein vollständiger Lauf über alle
Quellen. Er fängt ab, was inotify verpasst hat. Der nächste Lauf beginnt erst, wenn
der vorige fertig ist. Dauert ein Lauf länger als der Abstand, zeigt der Reiter das
an, ohne Alarm: Bei sehr großen Datenordnern bestimmt die Laufzeit den Abstand.

**Nur am aktiven Server:** Worker und Abgleich laufen nur in der Rolle aktiv und bei
eingeschalteter Spiegelung. Bei einem Rollenwechsel beendet der Worker laufende
Übertragungen. Auch mit „Automatik aus (Wartung)“ läuft die Spiegelung weiter.

**Erstabgleich:** Startet beim Einschalten, nachdem der freie Platz geprüft wurde.
Der Reiter zeigt den Fortschritt. Bis er fertig ist, gelten die Regeln aus Abschnitt 7.

**Löschen nur mit Beleg:** Welcher Server vollständig ist, weiß nur der Betreiber. Die
Vorgeschichte der Rollen entscheidet darum nicht über `--delete`.

- Hat der Empfänger für eine Quelle keinen eigenen Beleg, läuft der Lauf ohne `--delete`. Ein
  `--delete` ohne Beleg und ohne Bestätigung lehnt der Empfänger ab.
- War sein Ziel beim Start des Laufs leer (ausgeschlossene Ordner und leere Ordner zählen nicht,
  ein Lesefehler zählt als nicht leer), entsteht der Beleg nach dem Lauf von selbst. Auch ein
  abgebrochener Lauf zählt beim nächsten weiter als Lauf auf ein leeres Ziel. Ein neuer Partner
  braucht also keine Bestätigung. Sofort-Übertragungen belegen nichts.
- War das Ziel nicht leer, wartet die Quelle: ein einmaliger Alarm, der Reiter zeigt
  „wartet auf Bestätigung“ mit der Anzahl und je Quelle. Eine wartende Quelle kommt nur über die
  Bestätigung heraus, nie über ein später leeres Ziel.
- Die Bestätigung (`pdh-ha mirror confirm`, Knopf „Dieser Server ist vollständig“) gilt am
  aktiven Server und nur für die in dem Moment wartenden Quellen; am passiven Server und ohne
  wartende Quelle wird sie abgelehnt. Eine später wartende Quelle meldet sich einzeln. Sie gilt für
  die aktuelle Datenkennung des Empfängers, bleibt über einen Neustart erhalten und ist verbraucht,
  sobald der Empfänger den Beleg meldet. Der nächste Lauf löscht, abweichende Dateien am Partner
  wandern in seinen Papierkorb.
- Der aktive Server gilt selbst als belegt, sobald der Partner den Beleg meldet und die Quelle
  hier nicht unbelegt ist, mit dem echten Stand der Quelle (leer bleibt leer). Sonst stünde nach
  dem ersten Tausch jede Quelle auf „wartet“.
- Übernimmt ein Server im Erstabgleich ohne eigenen Beleg, ist die Quelle unbelegt und mündet in
  denselben Zustand. Ein eigener Beleg, den er später als Empfänger bekommt, hebt das auf.
- Ausschalten verwirft Belege, Bestätigungen und die gemerkte Rolle, weil die Stände dann
  auseinanderlaufen.
- **Datenträger:** Beleg und Quelle sind an die Gerätenummer (`st_dev`) des Ordners gebunden,
  sie steht im Zustand. Empfänger: Beleg und die Marke „leeres Ziel“ gelten nur auf demselben
  Datenträger, sonst wartet die Quelle wieder. Absender: Eine geänderte Nummer der Quelle heißt kein
  `--delete`, Alarm und Bestätigung. `lost+found` zählt nicht als Inhalt der Quelle. Fehlt die Nummer
  in einer älteren Zustandsdatei, wird sie beim ersten Lauf ohne Alarm nachgetragen. Es gibt
  keine Mengengrenze und kein `--max-delete`, gewolltes Aufräumen gäbe sonst Fehlalarme.

## 6. Nextcloud je Rolle

| Baustein | Aktiv | Passiv |
|---|---|---|
| PHP-FPM | läuft | gestoppt, kein Autostart |
| Cron (`/etc/cron.d`, alle 5 Minuten) | an | aus |
| Certbot-Zeitgeber | an | aus |
| nginx, Redis | laufen | laufen |

Die Steuerung kommt als weiterer `serviceCtl` in den `pgActor`, neben `svc` (pdh) und
`tun` (cloudflared), und folgt denselben Stellen: `stopServices`, Start nach dem
Hochstufen, `keepAutostartOff`. Ein Wartungsmodus wird nicht genutzt, denn er steht in
`config.php` und würde mitgespiegelt.

**Reihenfolge beim Wechsel:** Datenbank wird schreibbar, dann starten PHP-FPM und Cron.
Adresse und Tunnel folgen wie bisher.

**Nextcloud darf die Umschaltung nicht auslösen:** Startet Nextcloud nach dem Wechsel
nicht, gibt es einen Alarm, aber keine weitere Umschaltung. `pdh` bestimmt wie bisher
die Gesundheit des Servers.

**Nach dem Wechsel:** Web-Nutzer müssen sich eventuell neu anmelden, falls die Sitzungen
in Redis liegen; Redis wird nicht gespiegelt. Clients mit App-Passwort aus der
Datenbank melden sich von selbst wieder an.

### Adresse (Zugang über Cloudflare und im Firmennetz)

- Der aktive Server trägt die mitwandernde Adresse in `trusted_domains` ein, wenn sie
  fehlt. Das geschieht einmalig in `config.php` und wird mitgespiegelt.
- Cloudflare: Der Tunnel zeigt auf `localhost`, er wandert schon mit. Keine neue Logik.
- Der Reiter warnt, wenn das Server-Zertifikat die mitwandernde Adresse nicht enthält
  oder `PDH_NEXTCLOUD_BASEURL` die feste Adresse eines einzelnen Servers nennt. Das
  Zertifikat stellt der Anwender weiter über das PDH aus.

### Einrichtung: Verbund-Modus im Installationsskript

`scripts/install-nextcloud-nginx-postgres.sh` passt heute nicht in den Verbund:

| Stelle | Problem |
|---|---|
| `--restore` ruft `occ maintenance:data-fingerprint` auf | Zwingt alle Clients zum Neuabgleich; genau das soll der Verbund vermeiden |
| `--restore` ruft `occ maintenance:mode --off` auf | `occ` braucht die Datenbank, am passiven Server ist sie nur lesbar (im Plan prüfen) |
| `systemctl enable postgresql` läuft immer | `pdh-ha` steuert Postgres selbst, der Autostart bleibt aus |
| Normalmodus ruft `systemctl restart postgresql` auf | Startet die Datenbank des aktiven Servers im Betrieb neu |
| `--restore` verlangt `config.php` | Sie muss zuerst gespiegelt sein |

Neuer Schalter `--verbund` mit drei Fällen:

- **Aktiver Server, neue Nextcloud:** Anlegen wie bisher, ohne Postgres-Neustart und ohne
  Autostart.
- **Aktiver Server, vorhandene Nextcloud:** Nichts an der Instanz ändern, nur prüfen.
- **Passiver Server:** Nur Pakete, PHP und nginx, ohne `occ` und ohne Datenbankzugriff. Die
  Dateien kommen über die Spiegelung, PHP-FPM bleibt aus.

Der Verbund-Modus prüft außerdem, dass PHP- und Nextcloud-Fassung zusammenpassen und
sich die Rolle `nextcloud` am passiven Server anmelden darf, sobald er aktiv wird.

## 7. Geplanter Tausch und Update

- **„Rollen tauschen“ und Update mit Rollentausch:** Sobald die Dienste am abgebenden
  Server stehen und die Datenbank nur lesend ist, läuft ein letzter Abgleich, höchstens
  120 Sekunden lang (einstellbar). Erst dadurch kann nichts mehr nachkommen. Er überträgt
  die noch offenen Änderungen der Sofort-Quellen und die Konfigurations-Quellen; ohne
  inotify läuft ein voller Abgleich. Klappt das nicht, bricht der Tausch ab, die Dienste
  laufen wieder, und der Grund wird angezeigt. Das Vorgehen entspricht dem Warten auf die
  Datenbank. Diese Zeit verlängert die kurze Unterbrechung des Tauschs.
- **Erstabgleich nicht fertig:** „Rollen tauschen“ wird abgelehnt, und das Update gilt
  nicht als „alles grün“. Der Grund steht in der Meldung.
- **Spiegelung ausgeschaltet:** Keine Sperre, nur der Hinweis, dass Dateien fehlen können.
- **Echter Ausfall:** Eine automatische oder erzwungene Übernahme bleibt immer möglich.

## 8. Fehlerfälle

| Fall | Verhalten |
|---|---|
| Partner weg oder Übertragung scheitert | PDH und Nextcloud laufen weiter; neue Versuche; Alarm nach 5 Minuten (einstellbar), entprellt |
| Platte am Ziel voll | Spiegelung stoppt mit Alarm; Prüfung auch vor dem Erstabgleich |
| Zu viele Ordner für inotify | Der Abgleich läuft weiter; Warnung mit dem passenden `sysctl`-Befehl |
| Absender mit alter Amtszeit | Abgelehnt; Ereignis am Empfänger |
| Abbruch mitten in einer Datei | Die alte Fassung bleibt gültig; der nächste Lauf holt nach |
| Nextcloud- oder PHP-Fassung verschieden | Warnung im Reiter |
| `nginx -t` scheitert am Empfänger | Nicht neu geladen; Alarm; die alte Konfiguration läuft weiter |
| Nextcloud startet nach dem Wechsel nicht | Alarm, keine weitere Umschaltung |

**Alter aktiver Server kommt zurück:** Er wird passiv und damit Empfänger. Was er noch
nicht übertragen hatte, verschiebt der nächste Lauf in seinen Papierkorb. Der Reiter
meldet die Anzahl, gelöscht wird nichts still. Das Zurückholen bleibt Handarbeit.

**Harter Ausfall:** Der neue aktive Server vergleicht nach der Übernahme, was seit dem
letzten erfolgreichen Abgleich neu in `oc_filecache` steht, mit den Dateien auf der
Platte. Fehlende Dateien stehen im Reiter und lösen einen Alarm aus. Das gilt nur für
Nextcloud. PDH-Anhänge meldet der Prüflauf nur als Anzahl.

## 9. Einstellungen

Im Reiter Ausfallschutz, für den ganzen Verbund (`ha.Settings`), Änderungen wie die
übrigen Zeiten nur am aktiven oder ersten Server:

| Einstellung | Standard |
|---|---|
| Spiegelung an/aus | **aus**, damit ein Update niemanden überrascht |
| Nextcloud mitnehmen | aus |
| Abstand des Abgleichs | 1 Minute |
| Papierkorb aufbewahren | 14 Tage |
| Alarm nach | 5 Minuten |
| Frist für den letzten Abgleich | 120 Sekunden |
| Bandbreitengrenze | unbegrenzt |

## 10. Oberfläche und Schnittstellen

**Reiter Ausfallschutz, neue Karte „Datei-Spiegelung“:** Zustand, letzter erfolgreicher
Lauf je Quelle, Laufzeit, Erstabgleich mit Fortschritt, Warnungen aus den Abschnitten 3,
6 und 8, Papierkorb mit Größe und Knopf zum Leeren, Einstellungen. Wartet eine Quelle auf die
Bestätigung (Abschnitt 5), zeigt die Karte statt „bereit“ den Zustand „wartet auf Bestätigung“, die
Anzahl und die Quellen und, mit Recht und am aktiven Server, den Knopf „Dieser Server ist
vollständig“ mit Rückfrage. Neue Oberfläche wird
in Fiori und Fluent, hell und dunkel und bei 390 px geprüft.

**Befehle:** `pdh-ha mirror status`, `pdh-ha mirror run` (Abgleich sofort), `pdh-ha mirror confirm` (Abschnitt 5),
`pdh-ha mirror-pipe` (nur intern für rsync). `pdh-ha status` bekommt eine Zeile
„Spiegelung“, nicht am Zeugen.

**Heartbeat:** Optionale Felder für den letzten erfolgreichen Abgleich und die Prüfsumme
der `.env`, dazu `mirror_proven` (Quellen mit eigenem Beleg des Empfängers), `mirror_wait`
(Quellen, deren Ziel nicht leer war) und `up_lead` (Kennung des offenen Update-Auftrags, „-“ =
keiner). Ältere Versionen ignorieren sie. Fehlt `mirror_proven`, löscht der Absender nicht; fehlt
`up_lead`, bleibt der Handweg für einen fremden Auftrag gesperrt.

## 11. Rechte und Sicherheit

- Neues Recht `system.file_mirror` (Migration 155, Kategorie „System & Sicherheit“):
  Einstellungen ändern, Abgleich auslösen. Es geht an die Rollen mit
  `system.server_config`; niemand wird ausgesperrt.
- Den Papierkorb endgültig leeren und „Dieser Server ist vollständig“ bestätigen darf nur, wer
  das Bereichsadministrator-Recht `system.file_mirror_admin` hat (`areaAdminPerm`, `h.isAreaAdmin`).
  Die Bezeichnung nennt beides: „Datei-Spiegelung: Löschen am Partner freigeben und Papierkorb
  leeren“. Wer beides darf, kann den Rückweg über den Papierkorb selbst beenden. Einen allgemeinen
  System-Bereichsadministrator gibt es bisher nicht.
- Mitglieder des Verbunds vertrauen sich: Ein übernommener aktiver Server kann Dateien
  am passiven überschreiben, auch Skripte in `ha-hooks`, die dort als root laufen. Das
  entspricht dem bisherigen Vertrauen im Verbund, der Kanal erlaubt aber nur die Pfade
  aus Abschnitt 3. Die Prüfung auf Besitzer und Schreibrechte der Skripte bleibt aktiv.
- Der Zeuge bekommt keine Dateien und kann die Route nicht nutzen.

## 12. Tests und Abnahme

**Einzeltests:** Empfänger-Prüfungen aus Abschnitt 4, Quellenliste und Ausschlüsse, Sammeln
der inotify-Meldungen, Fälle aus Abschnitt 7, Dienste je Rolle, Prüflauf.

**Labor, neue Szenarien:**

- Datei hochgeladen: binnen Sekunden am passiven Server.
- Datei gelöscht: am passiven Server im Papierkorb.
- Tausch mit letztem Abgleich: keine Datei fehlt; Frist überschritten: Tausch abgebrochen.
- Harter Ausfall: Prüflauf nennt die fehlende Datei.
- Alter Server kommt zurück: seine Fassung im Papierkorb, Absender mit alter Amtszeit abgelehnt.
- Heartbeat unter Dateilast: keine falsche Umschaltung.
- Nextcloud als Attrappe: Dienste folgen der Rolle.
- Spiegelung aus: Verhalten wie bisher.

**Echtes Netz (.14/.17 mit Zeuge):** Dort ist keine Nextcloud installiert (geprüft am
03.11.; je 44 GB frei, 3,2 GB RAM). Die Abnahme installiert sie über `--verbund`, der Name
ist ein Parameter. Der Eintrag bei Cloudflare wird erst für die Abnahme angelegt. Geprüft
wird: Tausch und Ausfall mit Web, Desktop-Client und Handy-App, über den Tunnel und über
`10.10.0.15`; Freigabe-Link und Kalender nach dem Wechsel; Karten-Test wie bei `/32`.
**Auf 10.10.0.202 wird nichts getestet.**

## 13. Handbuch, Änderungsprotokoll, Version

- Kapitel Ausfallschutz: Der Hinweis „Hochgeladene Dateien werden noch nicht gespiegelt“
  entfällt. Neu sind So-geht's-Schritte zum Einschalten, zu Nextcloud im Verbund und zu den
  Fehlerbildern, dazu eine angepasste Skizze.
- Kapitel Administration und `### Sicherheit`: das neue Recht.
- Gesamt-Spec: Abschnitte 9 und 16 anpassen.
- Version: nächste Korrekturversion. Eine Nebenversion vergibt nur der Anwender.

## 14. Offene Punkte für den Plan

- Läuft `occ` überhaupt mit nur lesbarer Datenbank? (Abschnitt 6)
- Soll nach einem harten Ausfall `occ maintenance:data-fingerprint` laufen? Vorschlag: nicht
  automatisch, denn es zwingt alle Clients zum Neuabgleich; nur als Hinweis im Reiter, wenn
  der Prüflauf fehlende Dateien findet.
- Rsync-Option für unfertige Dateien und Umgang von `--files-from` mit Löschungen. Der
  Abgleich räumt gelöschte Dateien spätestens nach einem Abstand auf.
- Belastbarkeit von inotify bei vielen Ordnern.
- Liste der serverbezogenen `.env`-Schlüssel.
