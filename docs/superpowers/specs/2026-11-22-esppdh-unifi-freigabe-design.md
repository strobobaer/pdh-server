# ESPPDH: Freigabe für Updates übers Netz in UniFi anlegen – Entwurf

Stand 22.11.2026, zur Bestätigung durch den Anwender. Version: nächste freie
Korrekturversion (bei Beginn 0.82.27). Ergänzt
`2026-11-22-esppdh-ota-design.md`.

## Anlass und Ziel

Seit 0.82.25 spielt das PDH ESPPDH-Firmware übers Netz auf. Dafür muss der
Server das Gerät auf TCP-Port 3232 (ESP32) bzw. 8266 (ESP8266) erreichen. Liegen
Server und Gerät in getrennten Netzen, braucht es dafür eine Regel in der
Firewall des Gateways. Wunsch des Anwenders (22.11.2026): Das PDH trägt die
Regel in UniFi selbst ein – **auf Knopfdruck**. Ist kein Eintrag möglich, legt
es **ein Ticket an die IT** an.

## Ziele

- Knopf „Freigabe für ESPPDH-Updates in UniFi anlegen“ je Netz, das für
  ESPPDH-Geräte freigegeben ist und einen Adressbereich hat.
- Das PDH legt eine Firewall-Regel über die vorhandene UniFi-Anbindung an
  (offizielle Schnittstelle, API-Schlüssel) und liest sie zurück.
- Geht das nicht, entsteht ein Ticket an die IT mit allem, was zum Eintragen
  nötig ist.

## Nicht-Ziele

- Kein selbsttätiges Anlegen, kein Ändern oder Löschen vorhandener Regeln.
- Kein Omada (später auf demselben Weg möglich; bis dahin Ticket).
- Keine Firewall am Server selbst (für OTA ist der Server der Absender, dort
  ist nichts zu öffnen).

## Anti-Ziele

- Nie mehr freigeben als nötig: nur TCP 3232 und 8266, nur vom Server
  (beide Adressen im Verbund) zum Adressbereich des Geräte-Netzes, nur
  „Erlauben“.
- Keine doppelten Regeln und keine doppelten Tickets bei mehrfachem Drücken.

## Ansätze

- **A – eine Regel je Geräte-Netz** (gewählt): Quelle = Adressen des Servers
  (bei zwei Servern beide; nach einer Umschaltung geht es weiter), Ziel =
  Adressbereich des Netzes, TCP 3232 und 8266. Gilt für alle Geräte im Netz,
  auch neue und bei wechselnder Adresse.
- B – eine Regel je Gerät: verworfen (Adresse per DHCP wechselt, viele Regeln).
- C – ganze Zone freigeben: verworfen (gibt viel mehr frei als nötig).

## Ablauf beim Drücken

1. Prüfen, ob eine Regel nötig ist: Liegt die Adresse des Servers im
   Adressbereich des Netzes, reicht der Hinweis „keine Freigabe nötig“.
2. UniFi-Anbindung eingerichtet? Controller kann Firewall-Regeln (Network
   ab Version 10: `GET /v1/sites/{site}/firewall/zones` und
   `…/firewall/policies` antworten)? Gibt es die Regel schon (Name
   „PDH – ESPPDH-Update <Netz>“), ist nichts zu tun – Anzeige „Freigabe
   vorhanden“.
3. Zonen bestimmen: Zone des Server-Netzes und Zone des Geräte-Netzes (aus den
   Netzen der Zonen). Gleiche Zone: keine Regel nötig (UniFi erlaubt Verkehr
   innerhalb einer Zone in der Vorgabe) – Hinweis.
4. Regel anlegen (`POST …/firewall/policies`): Aktion „Erlauben“,
   Quelle = Server-Zone mit Adressfilter der Server-Adressen, Ziel =
   Geräte-Zone mit Adressfilter des Adressbereichs und Ports 3232, 8266,
   Protokoll TCP, eingeschaltet, Name wie oben. Danach zurücklesen.
5. Scheitert 2–4 (keine Anbindung, zu alte Version, keine Schreibrechte,
   Fehler der Schnittstelle, Zone nicht gefunden): **Ticket an die IT**.
   Empfänger wie bei den Netz-Alarmen der IT-Assets (eingestellte Person
   oder Gruppe, sonst Netzwerk-Administratoren). Inhalt: Netz, Adressbereich,
   Server-Adressen, Ports, Protokoll, Name der Regel und der Grund, warum das
   PDH sie nicht anlegen konnte. Ist für dieses Netz schon ein offenes
   Freigabe-Ticket da, kein zweites – Hinweis mit Verweis auf das Ticket.

## Oberfläche

- Seite „Netzwerk“, beim Netz: Zustand „Freigabe für ESPPDH-Updates:
  vorhanden / fehlt / nicht nötig / Ticket #… offen“ und der Knopf.
- Karte „Firmware übers Netz“ am Gerät: bei „fehlt“ ein Hinweis mit Verweis
  auf die Seite „Netzwerk“.

## Rechte

- Knopf nur mit `system.network_admin` (vorhandenes Recht – ändert die
  Firewall bzw. legt das Ticket an). Kein neues Recht.

## Daten

- Keine neue Tabelle nötig: die Regel wird beim Anzeigen über den Namen
  gesucht (Zwischenspeicher kurz), das offene Ticket über eine eigene
  Markierung (z. B. Schlagwort `esppdh-freigabe:<netz>`). Falls eine Spalte
  sauberer ist: Migration mit nächster freier Nummer.

## Dem Bauenden überlassen

- Genaue Feldnamen der Regel nach der Schnittstellen-Beschreibung der
  installierten Network-Version (vor dem Bau am Testverbund nur lesend
  prüfen).
- Zwischenspeicher und Zeitlimits der Abfragen.
