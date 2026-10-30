# Ausfallschutz (Einzel / Duo / Trio) – Gesamt-Architektur

Stand: 2026-10-24 · Status: Entwurf zur Freigabe

Vermerk: Teil 8 („Geplanter Rollentausch“) ist aufgegangen in
`2026-10-30-verbund-update-design.md` (Update im Verbund und Wartungsfenster).

## 1. Ziel

Größtmöglicher Ausfallschutz für PDH und die Admin-Werkzeuge („pdhadmin“: n8n,
pgAdmin, code-server, nginx) – vom Einzel-Server mit laufender Sicherung bis zur
kompletten Live-Spiegelung auf einen zweiten Server, der selbstständig übernimmt.

Anforderungen (vom Nutzer):

- Aktiv/Passiv; die Server entscheiden per Heartbeat selbst, wer aktiv ist.
- Zeuge (dritte Stimme) möglich, aber nicht Pflicht („alles kann, nichts muss“).
- Einzelbetrieb muss möglich sein: Sicherung laufend auf ein Netzlaufwerk,
  Wiederaufbau eines neuen Servers bzw. einer VM; zusätzlich VM-Sicherung des
  Hypervisors (Anleitung + Status, Hypervisor noch offen).
- Samba-Server auf beiden Servern, auch unabhängig vom PDH erreichbar.
- Dateien und Konfiguration per rsync gespiegelt; beide Systeme voll einsatzfähig.
- Alle Zeiten einstellbar.
- Produktion = systemd-Installation (Ubuntu, Postgres aus dem Paket);
  Docker nur als Testsystem.
- Benutzer erreichen das PDH heute über den Cloudflare-Tunnel.

Nicht-Ziele: Aktiv/Aktiv (gleichzeitiges Schreiben auf beiden), Multi-Master,
automatisches Zurückschalten auf den alten Server, direkte Hypervisor-API
(später möglich).

## 2. Betriebsarten

| Betriebsart | Aufbau | Schutz |
|---|---|---|
| Einzel | 1 Server (gern VM) | Grundsicherung + WAL-Archiv (Zeitpunkt-Wiederherstellung) und rsync von Dateien/Konfiguration aufs Netzlaufwerk; `pdh-restore.sh` baut neu auf; Anleitung + Status VM-Sicherung |
| Duo | 2 Server | Live-Spiegel, automatische Umschaltung im vorsichtigen 2-Knoten-Modus |
| Trio | 2 Server + Zeuge | Live-Spiegel, Umschaltung nach Mehrheit (Normalfall Produktion) |

- Wechsel zwischen den Betriebsarten ohne Neuinstallation.
- Die Netzlaufwerk-Sicherung läuft in jeder Betriebsart (bei Duo/Trio vom
  aktiven Server) – Schutz gegen Fehlbedienung und Datenfehler zusätzlich zur
  Spiegelung.
- **Zeugen-Regel:** Jede Stimme braucht eigene Hardware (möglichst eigener
  Strom/Raum). Ein Zeuge als VM auf dem Host eines Knotens verhindert genau im
  Ernstfall die Übernahme (2 von 3 Stimmen fallen gemeinsam aus) und ist schlechter
  als Duo. Alle drei als VMs auf einem Host schützen nur gegen Software-Fehler
  (Testumgebung). Das PDH warnt bei erkennbar gemeinsamer Hardware.

## 3. Bausteine

- **`pdh-ha`** (`cmd/pdh-ha`, Go): systemd-Dienst unter root auf jedem Knoten;
  Modus `witness` für den Zeugen (auch als Container lauffähig). Steuert
  Postgres, Dienste, Firewall, virtuelle IP, rsync.
- **PDH ↔ pdh-ha:** lokaler Austauschordner bzw. Unix-Socket (Muster
  `pdh-wireguard`): Konfiguration hin, Status/Ereignisse zurück.
- **Knotenspezifisches** in `/etc/pdh/ha/` (Kennung, Zertifikate, eigene
  Adressen) – wird nie gespiegelt.
- **Update-Agent** richtet im Einzelbetrieb Postgres (WAL-Archiv) ein.

## 4. Rollen und Amtszeit

Rollen: `aktiv` · `passiv` · `abgeschaltet` (selbst gesperrt) ·
`wiedereingliederung` · `wartung` (Automatik aus) · `zeuge`.

Jede Umschaltung erhöht die dauerhaft gespeicherte **Amtszeit** (Term). Wer eine
höhere Amtszeit sieht, ordnet sich unter. Der Zeuge vergibt je Amtszeit genau
eine Stimme.

## 5. Heartbeat

- Takt einstellbar (Standard 1 s), gegenseitig zwischen allen Knoten über alle
  eingetragenen Wege (z. B. LAN und VPN), gegenseitiges TLS mit Zertifikaten der
  PDH-eigenen Zertifizierungsstelle.
- Inhalt: Rolle, Amtszeit, LSN/Verzögerung, synchron ja/nein, Zustand Postgres/
  PDH/Platte, Gateway erreichbar, Programmversion.

## 6. Entscheidungsregeln

Alle Zeiten einstellbar; Standardwerte in Klammern. Das PDH lehnt Werte ab, bei
denen gilt: Übernahme < Selbst-Abschaltung + 2 × Heartbeat + 5 s.

**Trio**

- Aktiv bleibt nur, wer die Mehrheit sieht (Partner oder Zeuge). Ohne Mehrheit
  für die Selbst-Abschaltzeit (10 s) → `abgeschaltet`: Postgres nur lesend,
  PDH-Wartungsseite, Samba nur lesend, cloudflared aus, virtuelle IP abgeben.
- Passiv übernimmt, wenn der aktive Server für die Übernahmezeit (30 s) auf
  allen Wegen fehlt, der Zeuge seine Stimme gibt und der eigene Stand aktuell
  (synchron) ist.

**Duo**

- Aktiv: sieht er weder Partner noch Gateway für 10 s → `abgeschaltet`.
- Passiv übernimmt nach 60 s ohne Partner nur, wenn er selbst das Gateway
  erreicht und synchron war.
- Restrisiko (Direktverbindung weg, beide sehen das Gateway) wird dauerhaft als
  „eingeschränkter Ausfallschutz (kein Zeuge)“ angezeigt.

**Datenverlust vermeiden**

- Beide gesund → synchrone Replikation (`synchronous_standby_names`).
- Passiver weg → nach der Zeit „asynchron nach“ (Standard 10 s) asynchron
  weiterarbeiten; passiver gilt als „nicht aktuell“ und übernimmt nie
  automatisch, nur per „Übernahme erzwingen“ mit Warnung.

**Schutz vor Hin-und-Her**

- Kein automatisches Zurückschalten.
- Wartezeit nach Umschaltung (15 min); höchstens N Umschaltungen pro Tag (3),
  danach `wartung` + Alarm.

**Geplanter Wechsel** („Rollen tauschen“): aktiver wartet auf Gleichstand, gibt
ab, passiver übernimmt – ohne Datenverlust, Unterbrechung Sekunden.

**Rückkehr**

- Ausgefallener Knoten sieht höhere Amtszeit → `wiedereingliederung`:
  `pg_rewind`, bei Fehlschlag vollständige Neukopie (`pg_basebackup`).
- Nur dort vorhandene Daten (nur bei asynchronem Betrieb möglich) werden vorher
  als „abgezweigte Daten“ gesichert und im PDH zur Prüfung angezeigt; nie
  stillschweigend gelöscht.

**Alarm:** Umschaltung, Selbst-Abschaltung, Verzögerung über Grenzwert,
Spiegelfehler → Admins über PDH-Benachrichtigungen/Push (vom aktiven Server).

## 7. Dienste je Rolle

| Dienst | aktiv | passiv |
|---|---|---|
| Postgres | schreibend | Hot-Standby, lesend |
| PDH | normal | Lesemodus: Anmelden/Ansehen, Speichern gesperrt, Banner „Spiegel – nur lesen“, keine Hintergrundjobs/Migrationen |
| n8n, Zeitpläne, Netzlaufwerk-Sicherung | an | aus |
| pgAdmin | an | an (lesend) |
| code-server | an | aus |
| Samba | lesen + schreiben | nur lesen |
| cloudflared | an | aus |

Beide Knoten bleiben unter ihrer eigenen Adresse direkt erreichbar (PDH und
Samba lesend).

## 8. Zugangsweg

- **Cloudflare-Tunnel (Standard):** cloudflared nur auf dem aktiven Knoten.
- **Virtuelle IP (optional, für LAN und Samba nötig):** `pdh-ha` übernimmt sie
  per Gratuitous ARP; nur bei gleichem Netzsegment.
- **DNS-Skript (optional):** frei hinterlegtes Skript beim Rollenwechsel.

Samba ist über Cloudflare nicht erreichbar – bei aktivem Samba fragt die
Einrichtung virtuelle IP oder DNS-Skript ab.

## 9. Spiegelung (rsync)

- Aktiv → passiv über SSH mit eigenem, auf rsync beschränktem Schlüssel.
- Intervall einstellbar (Standard 1 min), zusätzlich sofort nach Uploads.
- Inhalt: Uploads, Chat-Dateien, `/etc/pdh` (ohne `/etc/pdh/ha/`), n8n-Daten,
  pgAdmin-/code-server-Einstellungen, nginx-Seiten, cloudflared-Konfiguration,
  Samba-Freigaben und -Benutzer.
- Papierkorb: gelöschte/überschriebene Dateien auf dem Ziel per `--backup-dir`,
  Aufbewahrung einstellbar (Standard 14 Tage).
- Richtung folgt der Rolle; der neue aktive Knoten nimmt nichts mehr vom alten an
  (Amtszeit).
- Live-Datenbanken anderer Programme (z. B. SQLite von n8n) werden vor dem
  Kopieren konsistent gesichert, nie roh kopiert.

## 10. Samba

- Auf beiden Knoten; Freigaben und Samba-Benutzer im PDH verwaltet und
  gespiegelt.
- Erreichbar auch ohne laufendes PDH-Programm.
- Schreibzugriff nur auf dem aktiven Knoten (über virtuelle IP bzw. gemeinsamen
  Namen); `pdh-ha` schaltet `read only` beim Rollenwechsel um.

## 11. Einzelbetrieb im Detail

- Netzlaufwerk mit Rolle Sicherung (vorhandene Funktion Netzlaufwerke).
- Update-Agent: `archive_mode`/`archive_command` aufs Laufwerk; Grundsicherung
  (`pg_basebackup`) nach Zeitplan (Standard täglich); Aufbewahrung (Standard
  14 Tage) inkl. Aufräumen alter WAL-Dateien.
- rsync Dateien/Konfiguration (Standard stündlich, einstellbar).
- `pdh-restore.sh` (Download im PDH und Kopie auf dem Laufwerk): frischer
  Ubuntu-Server/VM → komplettes System, wahlweise neuester Stand oder Zeitpunkt.
- Handbuch: allgemeine Anleitung VM-Sicherung; Status „Letzte VM-Sicherung“
  (von Hand oder Zeitstempel einer Datei auf der Freigabe).
- Die bestehende ZIP-Datensicherung bleibt unverändert daneben bestehen.

## 12. Einrichtung Duo/Trio

1. „Ausfallschutz einrichten“ auf dem bestehenden Server: Zertifizierungsstelle
   und zeitlich begrenzter Beitrittscode.
2. `pdh-ha-join.sh` auf frischem Ubuntu: gleiche PDH-Version, gleiche
   Postgres-Hauptversion (notfalls PGDG-Paketarchiv), Samba, rsync, pdh-ha;
   Erstkopie Datenbank + erster Dateiabgleich.
3. Optional Zeuge: `pdh-ha-witness.sh` oder Container.
4. Pflicht-Testumschaltung (tauschen und zurück); bis dahin `wartung`.

**Rollende Updates:** passiven Knoten aktualisieren → Rollen tauschen (Migrationen
auf dem neuen aktiven) → anderen Knoten aktualisieren. Automatische Umschaltung
nur zwischen gleichen Programmversionen.

## 13. Oberfläche

Server-Einstellungen → Reiter „Ausfallschutz“ (nur `canServerConfig`):
Assistent (Betriebsart), Übersicht (Knoten, Rolle, Amtszeit, Verzögerung,
synchron, Heartbeat je Weg, Version, Zeuge, Zugangsweg, letzter Spiegellauf,
Papierkorb, Ereignisse), Knöpfe „Rollen tauschen“, „Wartung an/aus“, „Knoten
entfernen“, „Übernahme erzwingen“ (Warnung + Bestätigung), alle Zeiten.
Neues Handbuch-Kapitel „Ausfallschutz“ (Einrichtung, Notfall, Wiederaufbau).

## 14. Testen

- Entscheidungslogik als reine Zustandsmaschine (ohne Netz/System), tabellarische
  Tests mit simulierter Uhr.
- Docker-Testlabor (2 Knoten + Zeuge) mit Ausfall-Skript: aktiver weg, Leitung
  getrennt, Zeuge weg, Hin-und-Her, Rückkehr mit `pg_rewind`, synchron →
  asynchron, Massenlöschung, rollendes Update. Invarianten: nie zwei aktive
  Knoten; keine bestätigte Speicherung verloren.
- Produktion: Pflicht-Testumschaltung bei Einrichtung.

## 15. Teilprojekte und Reihenfolge

Jedes Teilprojekt bekommt eigene Detail-Spezifikation und Plan.

1. **Einzelbetrieb** – WAL-Archiv, Grundsicherung, rsync aufs Netzlaufwerk,
   `pdh-restore.sh`, VM-Sicherungs-Anleitung/-Status (vorgezogen: sofortiger
   Schutz des heutigen Servers).
2. **pdh-ha-Kern** – Zustandsmaschine, Heartbeat, Zeuge, Testlabor.
3. **Datenbank-Cluster** – Replikation, synchron/asynchron, Umschaltung,
   `pg_rewind`, abgezweigte Daten.
4. **Lesemodus des PDH**.
5. **Zugangsweg** – Cloudflare, virtuelle IP, DNS-Skript.
6. **Spiegelung** – rsync + Papierkorb.
7. **Samba**.

Oberfläche, Assistent und Handbuch wachsen mit jedem Teilprojekt mit.

## 16. Offene Punkte (in den Detail-Spezifikationen zu klären)

- Postgres-Hauptversion der Produktion (Ubuntu 22.04 = 14, 24.04 = 16) und
  Umgang mit gemischten Versionen.
- Weitere Datenbanken im selben Postgres (z. B. Nextcloud) werden mitrepliziert –
  gewollt; Nextcloud selbst ist nicht Teil der Umschaltung.
- Speicherort und Format von n8n-Daten auf den Produktivservern.
