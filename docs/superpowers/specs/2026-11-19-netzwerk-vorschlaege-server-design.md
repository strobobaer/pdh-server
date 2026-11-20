# Seite „Netzwerk“: Vorschläge aus UniFi/Omada und „Dieser Server“ (0.82.4) – Entwurf

Stand 19.11.2026. Wünsche des Anwenders (19.11.2026):

- „Bei Verbindung zu UniFi sollte er doch die Netzwerkliste selbstständig
  ausfüllen können, dass es nur noch bestätigt werden muss – ebenso bei
  Omada.“ WLAN-Kennwörter werden mit übernommen („ja“, Handeingabe als
  Rückfall).
- „Unter Netzwerk die servereigene IP-Adresse auch sehen und/oder bearbeiten,
  auf statisch setzen usw.“ – **Stufe 1 reicht** („ja“): ansehen, jetzige
  Adresse fest einstellen, DNS-Server ändern. Eine **andere** Adresse
  vergeben ist kein Auftrag.

Version **0.82.4** (nach 0.82.3), Migration **184**.

**Vom Anwender am 19.11.2026 freigegeben („Passt“)**, samt der Festlegungen
unten.

## Ausgangslage (0.82.2)

- Seite `/admin/network` (0.81.4): Netze (Name, VLAN, Adressbereich, Zweck,
  Notiz) und WLANs (SSID, Netz, Kennwort verschlüsselt, versteckt, Notiz,
  Haken „Für ESPPDH-Geräte nutzbar“) – nur Handeingabe. Rechte
  `system.network` (pflegen), `system.network_admin` (löschen).
- UniFi- und Omada-Anbindung lesen heute nur Geräte, Teilnehmer und (UniFi)
  die Namen im Haus; Netze und WLANs fragt keine ab.
- Feste Adresse: nur von Hand per SSH mit `scripts/pdh-static-ip.sh`. Das
  Skript setzt sich nur im Dialog an der Konsole selbst zurück
  (`netplan try`); mit `--yes` ohne Rückfall. Es kann DNS nicht allein
  ändern und erkennt nicht, ob die Adresse heute fest oder per DHCP ist.
- Im Verbund kennt das PDH die Adressen des Partners nur aus dem
  Ausfallschutz (Wege), nicht dessen Gateway, DNS oder fest/DHCP. Der
  Update-Agent jedes Servers ist nur lokal erreichbar.

## Teil A – Vorschläge aus UniFi und Omada

### Was der Anwender sieht

Ist UniFi und/oder Omada verbunden, steht oben auf der Seite ein Block
**„Vom Controller gefunden“** (lädt nach, blockiert die Seite nicht):

```
Vom Controller gefunden                         [Neu abfragen]
UniFi · vor 2 Min.
  NEU      Netz  Produktion   VLAN 20  10.10.20.0/24     [Übernehmen] [Ausblenden]
  ANDERS   Netz  Büro         VLAN 10  PDH: 10.10.1.0/24 → 10.10.10.0/24  [Angleichen]
  NEU      WLAN  Werk-IoT     → Produktion  WPA2  Kennwort ✓  [Übernehmen] [Ausblenden]
  NEU      WLAN  Gast         → Gast  offen                   [Übernehmen] [Ausblenden]
  3 Einträge stimmen schon überein.                       [Alle übernehmen]
Omada · nicht erreichbar: Zeitüberschreitung
```

- **NEU:** gibt es im PDH noch nicht → „Übernehmen“ legt es an.
- **ANDERS:** gibt es, weicht ab → „Angleichen“ übernimmt die Werte des
  Controllers (Name, VLAN, Adressbereich bzw. Netz, versteckt, Kennwort);
  Zweck, Notiz und der ESPPDH-Haken bleiben.
- **Ausblenden:** der Vorschlag erscheint nicht mehr, bis er sich am
  Controller ändert (gemerkt je Quelle und Kennung beim Controller).
- „Alle übernehmen“ übernimmt alle NEU- und ANDERS-Zeilen der Quelle.
- Nichts wird ohne Klick angelegt, geändert oder gelöscht. Was am Controller
  verschwindet, bleibt im PDH; am Eintrag steht „am Controller nicht mehr
  vorhanden“.
- Den Haken „Für ESPPDH-Geräte nutzbar“ setzt das PDH nie selbst.
- Übernommene Einträge tragen eine kleine Herkunft („aus UniFi“).

### Zuordnung

- Netz ↔ Netz: zuerst über die gemerkte Kennung beim Controller, sonst über
  die VLAN-Nummer, sonst über den Namen (ohne Groß/Klein).
- WLAN ↔ WLAN: Kennung beim Controller, sonst SSID.
- Das Netz eines WLANs: über die Netz-Kennung (UniFi) bzw. die VLAN-Nummer
  (Omada); gibt es das Netz im PDH noch nicht, wird es beim Übernehmen des
  WLANs mit angelegt (Hinweis in der Zeile).
- Sind UniFi und Omada beide verbunden, werden beide getrennt zugeordnet;
  was eine Quelle schon angelegt hat, erscheint bei der anderen als ANDERS
  oder „stimmt überein“.

### Kennwörter

- Gibt der Controller das Kennwort heraus (WPA2/WPA3 mit gemeinsamem
  Kennwort), wird es beim Übernehmen verschlüsselt gespeichert – wie bei der
  Handeingabe. Es geht **nie** an den Browser, in kein Protokoll und in keine
  Fehlermeldung; der Vorschlag zeigt nur „Kennwort ✓“.
- Fehlt es (nicht herausgegeben, verdeckt, Enterprise mit Benutzeranmeldung):
  WLAN ohne Kennwort übernehmen, Hinweis „Kennwort bitte von Hand
  eintragen“. Enterprise-WLANs tragen zusätzlich „für ESPPDH nicht nutzbar“.
- Kennwort beim Controller geändert: Zeile ANDERS („Kennwort geändert“), ohne
  das Kennwort zu zeigen.

### Abfrage beim Controller (nur lesen)

- **UniFi** (offizielle Schnittstelle der Network-Anwendung, ab Version
  10.0): `GET …/integration/v1/sites/{siteId}/networks` und je Netz die
  Einzelabfrage (Adressbereich); `GET …/sites/{siteId}/wifi/broadcasts` und
  je WLAN die Einzelabfrage (Kennwort, verstecktes Netz). Antwortet die
  Network-Anwendung mit „nicht gefunden“: Hinweis „UniFi-Network-Anwendung zu
  alt – Netze und WLANs gibt es ab Version 10.0“. Keine Ausweichpfade über die
  alte Schnittstelle.
- **Omada** (OpenAPI): Netze über `…/sites/{siteId}/lan-networks`
  (v3, ersatzweise v2, v1), WLANs über `…/wireless-network/ssids` (v2) und
  je WLAN die Einzelabfrage (Kennwort).
- Felder, die am Gerät anders heißen oder fehlen, bleiben leer statt Fehler.
  Zeitgrenze 10 s je Quelle; Fehler je Quelle im Block, nie Abbruch der
  Seite.
- Ergebnis wird **5 Minuten im Speicher** des Servers gehalten
  („Neu abfragen“ holt sofort neu). Kennwörter liegen nur in diesem
  Speicher, bis übernommen wird.
- **Nicht geklärt, nur am Gerät prüfbar:** ob UniFi und Omada das Kennwort
  im Klartext herausgeben und mit welchen Rollen des Zugangs; genaue
  Feldnamen bei Omada; die niedrigste Omada-Version für v3/v2. Der
  Rückfall (Handeingabe) deckt das ab.

### Rechte

- Vorschläge sehen, übernehmen, angleichen, ausblenden: `system.network`
  (wie die Handeingabe). Kein neues Recht.
- Am Spiegel: Block sichtbar, Knöpfe gesperrt (nur lesen).

## Teil B – Karte „Dieser Server“

### Was der Anwender sieht

Oben auf der Seite eine Karte je Server – im Verbund beide nebeneinander:

```
pdh-server (aktiv)                       pdh (Spiegel)
Netzwerkkarte eth0                       Netzwerkkarte eth0
Adresse     10.10.0.17/24  fest          Adresse  10.10.0.14/24  fest
Gemeinsame  10.10.0.15  hier             Gemeinsame  –
Gateway     10.10.0.1                    Gateway  10.10.0.1
DNS         10.10.0.1, 1.1.1.1           DNS      10.10.0.1, 1.1.1.1
IPv6        2a02:…::17                   IPv6     2a02:…::14
[DNS-Server ändern]                      [Auf pdh öffnen ↗]
```

- „fest“ / „automatisch (DHCP)“ / „unbekannt“.
- Knöpfe nur auf der Karte des Servers, auf dem die Seite gerade läuft. Für
  den anderen Server ein Verweis auf seine eigene Adresse („Auf pdh öffnen“);
  dort gelten dieselben Knöpfe (auch am Spiegel erlaubt, wie beim Einrichten
  der ESPPDH-Umgebung).
- Im Docker-Testsystem: „Im Docker-Testsystem nicht verfügbar“, keine Knöpfe.

### Die zwei Aktionen

1. **„Jetzige Adresse fest einstellen“** – nur bei „automatisch (DHCP)“.
2. **„DNS-Server ändern“** – ein bis drei IPv4-Adressen. Stellt die jetzige
   Adresse dabei ebenfalls fest ein (steht im Dialog).

Ablauf für beide:

1. **Vorschau:** Der Dialog zeigt, was geschrieben wird (Netzwerkkarte,
   Adresse, Gateway, DNS) und die Nebenwirkungen: automatische Konfiguration
   durch cloud-init wird abgeschaltet; am aktiven Server ist die gemeinsame
   Adresse für einige Sekunden weg; Empfehlung, zuerst den Spiegel zu ändern.
2. **DNS-Prüfung** (nur bei „DNS ändern“): jeder neue DNS-Server muss auf eine
   Anfrage antworten; sonst Abbruch mit Hinweis, nichts wird geschrieben.
3. **Anwenden mit Rückfall:** Der Server schreibt die Einstellung, wendet sie
   an und stellt sich einen Wecker auf **2 Minuten**. Die Seite zeigt einen
   Countdown und **„Seite erreichbar – Einstellung behalten“**. Wird nicht
   bestätigt (Verbindung weg, Fenster zu), stellt der Server die vorige
   Einstellung von selbst wieder her. Der Stand bleibt nach Neuladen der
   Seite sichtbar.
4. Ergebnis und Benutzer stehen im Server-Protokoll.

Gesperrt, solange ein PDH-Update, eine Wartung des Ausfallschutzes oder ein
Rollentausch läuft (Hinweis mit Grund).

### Woher die Daten kommen

- **Eigener Server:** neuer Abruf beim Update-Agenten (läuft als root):
  Adressen, Standardroute, DNS-Server, fest/DHCP (DHCP-Adressen tragen im
  System das Merkmal „dynamisch“), Netzwerkkarte.
- **Partner:** der Ausfallschutz-Dienst jedes Servers ermittelt dieselbe
  Zusammenfassung selbst und schickt sie mit seinem ohnehin laufenden
  Lebenszeichen; das PDH liest sie aus dem Status des Ausfallschutzes.
  Dieselbe Auswertung (ein gemeinsames Paket) für beide Wege.
- Ein Server ohne Verbund: nur die eigene Karte.

### Umsetzung der Aktionen (Vorgaben)

- `scripts/pdh-static-ip.sh` bekommt einen nicht-interaktiven Modus mit
  Rückfall-Wecker (statt `netplan try`) sowie „bestätigen“ und
  „zurücksetzen“; der bisherige Dialog an der Konsole bleibt unverändert.
  Andere Adressen bleiben im Verbund gesperrt (vorhandene Sperre).
- Der Update-Agent startet die Aktion als Hintergrund-Auftrag (Muster wie
  beim Einrichten der ESPPDH-Umgebung) und meldet den Stand; der Wecker läuft
  als eigener systemd-Auftrag, damit er einen Neustart des Agenten oder des
  PDH übersteht.
- Rechte: **Karte ansehen** mit `system.network`; **Aktionen** nur
  Administratoren mit `system.server_config` (wie Ausfallschutz und
  ESPPDH-Umgebung). Kein neues Recht.

## Migration 184

- `net_segments` und `net_wlans`: Herkunft (`source`: leer = von Hand,
  `unifi`, `omada`), Kennung beim Controller, Zeitpunkt der Übernahme.
- Ausgeblendete Vorschläge: Quelle, Kennung beim Controller, Fingerabdruck
  des Vorschlags (zeigt ihn wieder, wenn er sich ändert), wer, wann.

## Handbuch und Änderungsprotokoll

- Kapitel „Netzwerk“: „So übernehmen Sie Netze und WLANs vom Controller“,
  „So stellen Sie die Adresse fest ein / ändern die DNS-Server“, was der
  Countdown bedeutet. Der Abschnitt „So stellst du feste IP-Adressen ein“
  (Ausfallschutz) verweist auf die Karte; der Weg per SSH bleibt beschrieben.
- Skizze der Seite in `help_figures.gohtml` anpassen.
- CHANGELOG 0.82.4: `### Neu` (Vorschläge, Karte „Dieser Server“).

## Festlegungen (meine, bitte widersprechen, wenn anders gewünscht)

- Vorschläge werden beim Öffnen der Seite geholt (höchstens alle 5 Minuten),
  nicht im Hintergrund nach Zeitplan.
- Keine Ausweichpfade über die alte UniFi-Schnittstelle; UniFi braucht
  Network ab 10.0.
- Aktionen an der Server-Adresse nur mit `system.server_config` (Administrator),
  nicht mit dem breiter vergebenen `system.network`.
- „DNS ändern“ stellt die Adresse zugleich fest ein; DNS allein auf einem
  DHCP-Server gibt es nicht.
- Kein Ändern von Adresse oder Gateway in der Oberfläche (Stufe 2).

## Festlegungen nach der Gegenlese (19.11.2026, meine)

- **Ausfallschutz während der Aktion:** Die gemeinsame Adresse setzt der
  Ausfallschutz zur Laufzeit (nicht netplan) und übernimmt sie nach
  `netplan apply` von selbst wieder. Damit kurzer Ausfall von Gateway oder
  Adresse keinen Rollentausch auslöst, schaltet die Aktion am **aktiven**
  Server für ihre Dauer (Anwenden bis Bestätigen bzw. Rückfall) die
  **Wartung des Ausfallschutzes** ein und danach wieder aus. Am Spiegel ist
  das nicht nötig.
- **Unterstützte Systeme:** Aktionen nur auf Ubuntu mit netplan und
  systemd-networkd (Produktion). Andere Systeme: Karte zeigt Adresse,
  Gateway und DNS, Art „unbekannt“, keine Knöpfe, Hinweis „Fest einstellen nur
  auf Ubuntu-Servern mit netplan“.
- **Bestätigen:** geht vom Browser an das PDH **dieses** Servers und von dort
  an seinen Update-Agenten. Der Dialog nennt die Adresse, über die die Seite
  läuft, und empfiehlt für Aktionen die eigene Adresse des Servers. Startet
  das PDH während der 2 Minuten neu, zeigt die Seite nach dem Neuladen den
  Stand aus dem Agenten; der Wecker läuft unabhängig weiter.
- **Kennwort-Hinweise unterscheiden:** „offen“ (kein Kennwort),
  „Anmeldung mit Benutzer (Enterprise)“, „Controller gibt das Kennwort nicht
  heraus – bitte von Hand eintragen“. „Kennwort geändert“ wird nur erkannt,
  wenn der Controller das Kennwort liefert (Vergleich im Speicher mit dem
  entschlüsselten gespeicherten Kennwort; es wird nichts davon abgelegt).
- **Fingerabdruck ausgeblendeter Vorschläge** nur über nicht geheime Felder
  (Name, VLAN, Adressbereich, SSID, Sicherheit, versteckt, Netz) – nie über das
  Kennwort.
- **Am Spiegel** fragt der Block die Controller nicht ab; er zeigt „Vorschläge
  übernehmen am aktiven Server“ mit Verweis.
- **Verweis auf den Partner** („Auf pdh öffnen“) wird wie der vorhandene
  Verweis vom Spiegel zum aktiven Server gebildet (Adresse aus den Wegen des
  Ausfallschutzes, Anschluss des PDH). Ohne Verbund (auch Docker) gibt es
  keinen Partner und keinen Verweis.

## Dem Bauer überlassen

- Aufbau der Pakete (Abruf je Controller, gemeinsame Auswertung der
  Netzwerkdaten), Form des Speichers, Reihenfolge der Aufgaben.
- Wie der Ausfallschutz die Zusammenfassung im Lebenszeichen trägt (kleines
  Feld; ältere Partner ohne das Feld → „unbekannt“).
