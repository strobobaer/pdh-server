# Mitwandernde IPv6-Adresse – Entwurf

Stand: 2026-11-12 · Zweig `feature/mitwandernde-ipv6` · Version 0.80.1

## 1. Ziel

Geräte mit IPv6 im Haus sollen über den Namen (z. B. `nc.strobl-home.net`)
direkt zum aktiven Server gehen, nicht über Cloudflare.

Heute trägt das PDH an der UniFi UDM Pro je Name nur einen IPv4-Eintrag auf
die mitwandernde Adresse ein. Für IPv6 reicht die UDM Pro die Cloudflare-Adressen
durch; Geräte mit IPv6 nehmen deshalb den Weg über das Internet (gemessen am
12.11.2026 im Testverbund).

**Erfolg:**

- `nslookup -type=AAAA <Name> <UDM Pro>` liefert eine Adresse im Haus.
- Die Adresse liegt nur am aktiven Server und wandert beim Rollentausch und bei
  einer Übernahme mit.
- Der Aufruf über den Namen endet von einem IPv6-Gerät im Haus am Server.

## 2. Entscheidungen des Anwenders

- Die Adresse stammt aus dem Präfix des Anbieters und hat ein festes Ende
  (z. B. `2a02:f90:e198:1700::15`). Keine private Adresse (ULA).
- Eingeschaltet wird über ein Feld „IPv6-Ende“ neben der mitwandernden
  IPv4-Adresse. Leer heißt aus. Kein Schalter je Name.
- Ist das Feld gesetzt, bekommt jeder Name im Haus den IPv6-Eintrag automatisch
  – auch Namen, die von Hand in UniFi eingetragen wurden.

## 3. Rahmen

- **Zusatz.** Ohne IPv6 im Netz, ohne UniFi und ohne gesetztes Feld läuft alles
  wie bisher. Ein Fehler in diesem Teil hält nie einen Rollentausch auf, zählt
  nie als fehlgeschlagene Aktion und schaltet nie die Automatik ab.
- **Nur im Verbund** und nur zusammen mit eingeschalteter mitwandernder
  IPv4-Adresse. Einzelbetrieb gehört nicht dazu.
- **Nicht Teil:** private IPv6-Netze, Regeln an der Firewall der UDM Pro,
  IPv6 für den Verbund-Kanal (Port 7480) oder die Datenbank-Replikation.

## 4. Aufbau

### 4.1 Einstellung

`accessConfig` (`cmd/pdh-ha/access.go`) bekommt `vip6_suffix` (Text, leer = aus).

- Erlaubt ist der Rechnerteil einer IPv6-Adresse: gelesen als `::<Ende>`, die
  oberen 64 Bit müssen null sein, die unteren ungleich null. Beispiele: `15`,
  `0:0:0:15`, `aa:15`.
- `validateAccess` lehnt ein gesetztes Ende ohne eingeschaltete IPv4-Adresse ab.
- Das Feld steht im Reiter Ausfallschutz bei der mitwandernden Adresse
  (`internal/web/ha_access.go`, `web/templates/widgets/ha.gohtml`). Es hängt am
  vorhandenen Recht für diese Einstellungen. Kein neues Recht, keine Migration.

### 4.2 Verbund-Dienst

Neue Einheit `vip6Ctl` (`cmd/pdh-ha/vip6.go`), getrennt von `vipCtl`, gleiche
Netzwerkkarte wie die IPv4-Adresse.

- **Präfix:** aus `ip -6 -o addr show dev <Karte> scope global`. Es zählt ein
  `/64` einer öffentlichen Adresse, die nicht temporär, nicht „deprecated“ und
  nicht „tentative“ ist; die eigene mitwandernde Adresse zählt nicht. Bei
  mehreren Präfixen das mit der längsten Restlaufzeit. Ohne Präfix: keine
  Adresse, ein Hinweis im Status, kein Alarm je Takt (einmal melden).
- **Setzen:** `ip -6 addr add <Präfix>::<Ende>/128 dev <Karte> preferred_lft 0`.
  `/128` und `preferred_lft 0` sorgen dafür, dass der Server die Adresse nie als
  Absender wählt (Lehre aus dem Fehler vom 02.11.2026 mit der IPv4-Adresse).
- **Doppelte Adresse:** Die Prüfung des Kernels (DAD) bleibt an. Meldet er
  `dadfailed`, nimmt der Dienst die Adresse wieder weg und alarmiert einmal.
- **Ankündigung:** nach dem Setzen und noch einmal nach 5 s ein „Unsolicited
  Neighbor Advertisement“ mit Override an `ff02::1`. Gesendet aus dem Dienst
  selbst über einen ICMPv6-Socket (`golang.org/x/net`, schon im Modul). Im
  Labor wird gemessen, ob Linux- und Windows-Geräte damit sofort umlernen;
  reicht das nicht, entscheidet ein Ruling im Plan über die Ergänzung.
- **Reihenfolge:** Setzen nach der IPv4-Adresse, Wegnehmen mit ihr. Der Abgleich
  der Zugangswege (`access_worker.go`) ruft `vip6Ctl` nach `vipCtl` auf; ein
  Fehler von `vip6Ctl` wird gemeldet und sonst übergangen.
- **Präfixwechsel:** In jedem Takt des Abgleichs vergleicht der aktive Server
  Soll und Ist. Passt das Präfix nicht mehr, setzt er die neue Adresse, kündigt
  sie an und entfernt die alte. Ein passiver Server entfernt jede Adresse mit
  dem eingestellten Ende.
- **Feld geleert:** Beide Server entfernen die Adresse beim nächsten Takt.
- **Status:** `pdh-ha status` und die Status-Schnittstelle nennen die aktuelle
  IPv6-Adresse und wo sie liegt („Adresse 2a02:…::15: hier“).

### 4.3 PDH und UniFi

- `internal/integrations/unifi`: `Create`/`Update` nehmen neben IPv4 auch IPv6
  an (`AAAA_RECORD` mit `ipv6Address`; ältere Schnittstelle `record_type: AAAA`).
  `Record` trägt den Typ schon.
- **Welche Namen:** Es zählt, wohin ein Eintrag zeigt, nicht wer ihn angelegt
  hat (Vorgabe des Anwenders vom 12.11.2026: auch von Hand in UniFi
  eingetragene Namen werden nachgezogen). Jeder Name, der an der UDM Pro einen
  IPv4-Eintrag auf die mitwandernde IPv4-Adresse hat, bekommt den IPv6-Eintrag
  auf die mitwandernde IPv6-Adresse – auch Namen, die das PDH sonst nicht
  führt.
- **Vorhandene IPv6-Einträge:** Zeigt ein IPv6-Eintrag eines solchen Namens auf
  eine Adresse mit dem eingestellten Ende (also auf die mitwandernde Adresse
  unter einem früheren Präfix), ändert das PDH ihn auf die aktuelle – egal, wer
  ihn angelegt hat. Zeigt er woandershin, bleibt er unberührt; die Karte nennt
  ihn als Hinweis, „Überschreiben“ ändert ihn auf Rückfrage.
- **Nachziehen:** Ein Lauf alle 5 Minuten am aktiven Server liest die Einträge,
  legt fehlende IPv6-Einträge an und ändert abweichende nach den Regeln oben.
  Der Lauf macht nichts, solange keine UniFi-Anbindung bestätigt ist.
- **Aufräumen:** Das PDH merkt sich, welche IPv6-Einträge es selbst angelegt
  hat. Nur diese löscht es wieder: wenn das Feld geleert wird oder der Name
  keinen IPv4-Eintrag auf die mitwandernde Adresse mehr hat. Von Hand
  angelegte IPv6-Einträge löscht es nie.
- **Ohne UniFi:** Die Liste „Namen im Haus“ zeigt je Name zusätzlich den
  IPv6-Eintrag zum Eintragen von Hand, mit dem Satz, dass er sich ändert, wenn
  der Anbieter das Präfix wechselt.
- Änderungen an Einträgen stehen wie bisher im Protokoll „zertifikate“.

### 4.4 nginx

Die PDH-Seite des Update-Agenten (`cmd/update-agent/certs.go`) bekommt
`listen [::]:80;` und `listen [::]:443 ssl;`. Beim nächsten Abgleich schreibt
der Agent vorhandene Seiten neu. Die Nextcloud-Seite hört schon auf IPv6.
Hat der Server kein IPv6 (Kernel ohne IPv6), bleibt die Seite bei IPv4 – der
Agent prüft das vor dem Schreiben, damit nginx nicht am Start scheitert.

### 4.5 Firewall

`ufw` lässt Port 443 auf den Testservern schon für IPv6 zu; die Regeln setzt
das Installationsskript mit `ufw allow 443/tcp` für beide Familien. Keine
Änderung. Der Status meldet einen Hinweis, wenn `ufw` aktiv ist und IPv6 dort
abgeschaltet ist (`IPV6=no`).

## 5. Fehlerbilder

| Lage | Verhalten |
|---|---|
| Netz ohne IPv6-Präfix | keine Adresse, Hinweis im Status, IPv4 unverändert |
| Adresse schon vergeben | Adresse wieder weg, ein Alarm |
| `ip` meldet Fehler | ein Alarm, nächster Takt versucht es wieder |
| Präfix gewechselt | neue Adresse, alte weg; UniFi-Eintrag folgt binnen 5 Minuten |
| UDM Pro nicht erreichbar | Hinweis je Name wie bei IPv4, nächster Lauf versucht es wieder |
| Rollentausch, IPv6 scheitert | Tausch läuft durch, Alarm |

Zwischen Präfixwechsel und nachgezogenem Eintrag zeigt der Name auf eine
Adresse, die es nicht mehr gibt. Geräte fallen dann auf IPv4 zurück.

## 6. Sicherheit

Die Adresse ist öffentlich. Ob sie von außen erreichbar ist, entscheidet die
Firewall der UDM Pro; das PDH prüft das nicht. Das gilt heute schon für die
festen IPv6-Adressen beider Server. Das Handbuch sagt das ausdrücklich.

## 7. Prüfung

- **Tests:** Ende lesen und ablehnen, Präfixwahl aus `ip`-Ausgaben, Setzen,
  Wegnehmen, Präfixwechsel, `dadfailed`, Fehler ohne Wirkung auf den
  Rollentausch; UniFi-Einträge beider Typen; Nachzieh-Lauf; nginx-Seite mit und
  ohne IPv6.
- **Labor:** neues Szenario `vip6-tausch` im Docker-Labor mit IPv6-Netz:
  Adresse am aktiven, Tausch hin und zurück, Präfixwechsel, Feld leeren.
  Danach `alle` und der Race-Test.
- **Abnahme auf .14/.17:** Feld setzen, `nslookup -type=AAAA`, Aufruf über den
  Namen von einem IPv6-Gerät, Tausch in beide Richtungen, Windows-Gerät lernt
  um, Feld leeren entfernt Adresse und Einträge.

## 8. Handbuch und Version

- Handbuch: Kapitel Ausfallschutz (Feld „IPv6-Ende“) und Abschnitt „So zeigen
  die Namen im Haus auf den Server“ (IPv6-Eintrag, Präfixwechsel, Firewall).
- `CHANGELOG.md`: Eintrag 0.80.1 unter „Neu“.
