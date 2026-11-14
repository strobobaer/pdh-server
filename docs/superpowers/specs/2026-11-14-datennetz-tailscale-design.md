# Datennetz für Fremddatenbanken (Tailscale / Headscale) – Entwurf

Stand 2026-11-14 · Zusatzfunktion (optional, PDH muss ohne laufen)

## Absicht und Warum
Das PDH fragt Fremddatenbanken ab (Abgleich mit Fremdsystemen, Verbindungen unter `internal/web/conn_*.go`, Weg „direkt“ oder „vpn:<Tunnel>“ über WireGuard). Der Anwender will dafür ein **eigenes Datennetz** statt eines VPN-Tunnels ins Firmennetz: „Das Tailnet ist quasi das zusätzliche Datennetz.“ Auf jedem Datenbankserver läuft ein **PDH-Daten-Client**, der die Datenbank prüft und **nur sie** ins Datennetz lässt. Keine Portfreigaben, kein Zugriff auf ganze Netze.

## Ziele
- PDH-Daten-Client (Windows-Dienst und Linux-systemd-Dienst, ein einzelnes Programm) mit eingebautem Tailscale (Bibliothek `tsnet`): tritt einem Datennetz bei, bietet dort **nur den Datenbankport** an und reicht ihn an die lokale Datenbank weiter (TCP-Weiterleitung, datenbankunabhängig: SQL Server, PostgreSQL, MySQL, Oracle …).
- Der Client prüft regelmäßig, ob die Datenbank lokal erreichbar ist (TCP-Verbindung zum Port; die Anmeldung prüft das PDH selbst über das Datennetz mit den in der Verbindung gespeicherten Zugangsdaten – der Client kennt keine Datenbank-Passwörter) und meldet Status, Version und letzte Prüfung ans PDH.
- **Zwei Arten von Datennetz, wahlweise je Client, auch gleichzeitig** (Anwender):
  - **intern** – Headscale, vom PDH mitgebracht und verwaltet, nur im Firmennetz erreichbar.
  - **Cloud** – das vorhandene Tailscale-Tailnet des Anwenders (tailscale.com), für Standorte/Kunden über das Internet.
- Das PDH tritt beiden Netzen im eigenen Prozess bei (je Netz eine `tsnet`-Instanz); am Netz der Server ändert sich nichts.
- Jede Datenbankverbindung bekommt als weiteren Weg „Datennetz: <Client>“ neben „direkt“ und „vpn:<Tunnel>“. Abgleich, Zeitgrenzen und die Prüfung „Weg bereit“ bleiben wie heute.
- **Updates des Clients über den Update-Dienst des PDH** (Anwender): Jede PDH-Version bringt den passenden Client (Windows/Linux, gleicher Commit) mit; der Client fragt über das Datennetz seine Soll-Version ab, lädt die neue Datei vom PDH, prüft die Signatur, tauscht sich aus und startet neu; schlägt der Start fehl, zurück zur alten Fassung. Automatisch mit dem PDH-Update oder per Knopf je Client.
- Übersicht im PDH: je Client Netz, Name, Version, letzter Kontakt, Datenbank erreichbar, Update ansteht/fehlgeschlagen; Client anlegen (Einmal-Schlüssel bzw. Installationsbefehl), sperren, entfernen.

## Nicht-Ziele
- Kein allgemeines VPN, keine Subnetz-Router, kein Zugriff auf andere Ports des Datenbankservers.
- Headscale nicht aus dem Internet erreichbar (keine Portfreigabe); entfernte Standorte nutzen das Cloud-Netz.
- Der WireGuard-Weg bleibt unverändert bestehen.

## Anti-Ziele (wäre ein Fehlschlag, auch wenn es technisch läuft)
- Der Datenbankserver hängt als ganzer Rechner im Netz.
- Der Client wird zum vergessenen Altbestand ohne Updates.
- Datenbank-Passwörter liegen auf dem Client.
- Das PDH startet nicht oder hängt, wenn Datennetz, Headscale oder Tailscale ausfallen.

## Rahmen
- Go wie das PDH; `tsnet` und Headscale sind Go.
- **Verbund (HA):** Headscale-Daten in der replizierten Postgres des PDH (eigene Datenbank `headscale`); Headscale läuft nur auf dem aktiven Server unter der gemeinsamen Adresse und wandert beim Rollentausch mit; der Spiegelserver startet es nicht. Der Knoten-Schlüssel des PDH im Datennetz wandert beim Rollentausch mit (in der Datenbank gespeichert, nicht nur auf der Platte), damit Clients nichts neu freigeben müssen.
- Rechte: neuer Bereich mit `datanet.view`, `datanet.edit`, `datanet.admin` (Bereichsadministrator; Entfernen nur admin), per Migration den Rollen gegeben, die heute Verbindungen bzw. VPN verwalten. Den Weg „Datennetz“ an einer Verbindung wählt, wer die Verbindung bearbeiten darf.
- Freigaben: Intern erzeugt das PDH die Headscale-ACL selbst (nur der PDH-Knoten erreicht die Client-Ports). Cloud: Clients bekommen ein Tag (z. B. `tag:pdh-data`), das PDH `tag:pdh`; die ACL im Tailnet muss `tag:pdh → tag:pdh-data` erlauben – das PDH zeigt die nötige ACL-Zeile an und prüft, ob die Verbindung klappt; mit API-Schlüssel erzeugt es Einmal-Schlüssel selbst, sonst Eingabe von Hand.
- Signatur der Client-Updates: eigener Schlüssel des PDH (verschlüsselt in `app_settings` wie die PKI); der Client vertraut dem öffentlichen Schlüssel aus der Erstinstallation.
- Handbuch, CHANGELOG, Fiori/Fluent wie üblich (AGENTS.md).

## Ansätze (besprochen)
- **A – eigener PDH-Daten-Client mit eingebautem Tailscale (gewählt).** Eine Datei, nur der Datenbankport sichtbar, Status und Updates im PDH.
- B – offizielles Tailscale an beiden Enden plus Prüf-Client: zwei Installationen, ganzer Rechner im Netz, Regeln außerhalb des PDH.
- C – Subnetz-Router am Standort: ganzes Subnetz sichtbar, keine Datenbankprüfung; widerspricht dem Wunsch.
- Steuerung: **beides** – Headscale im PDH (intern) und vorhandenes Tailnet (Cloud), wahlweise je Client, parallel.

## Dem Umsetzer überlassen


- Takt von Prüfung und Statusmeldung (Vorschlag 60 s) und Update-Abfrage (Vorschlag 15 min).


## Entscheidungen nach der Prüfung (Leiter, vom Anwender gegenzulesen)
1. **Headscale als eigener Prozess**, nicht im PDH eingebettet (keine stabile Bibliotheks-API, Versionskonflikt mit `tailscale.com`). Der Update-Agent installiert und aktualisiert ihn; das PDH steuert ihn über dessen API mit API-Schlüssel. Er läuft nur auf dem aktiven Server; Daten in der Datenbank `headscale` der replizierten Postgres.
2. **Ein PDH-Knoten je Server** (eigene Identität, beide mit `tag:pdh` in der Freigabe) statt eines wandernden Knoten-Schlüssels. Nur der aktive Server baut Verbindungen auf; beim Rollentausch ist der andere sofort bereit, ohne dass zwei Knoten mit demselben Schlüssel gleichzeitig online sind.
3. **Weg zu den Treibern über eine lokale Brücke:** Für eine Verbindung mit Weg „Datennetz“ öffnet das PDH `127.0.0.1:<Zufallsport>` und leitet ins Datennetz weiter; alle Treiber (SQL Server, PostgreSQL, MySQL, Oracle) verbinden sich dort, ohne Treiber-Sonderwege. Gespeichert als `route=net:<Client-ID>`; die Adressprüfung des VPN-Wegs entfällt, weil das Ziel am Client fest liegt. TLS-Prüfungen (z. B. SQL-Server-Fingerabdruck) bleiben die des Datenbankservers.
4. **Rückkanal für Status und Updates:** Das PDH lauscht im Datennetz auf genau einem eigenen Port (Vorschlag 8443, nicht die Weboberfläche). Freigabe: `tag:pdh → tag:pdh-data` (Datenbankports) und `tag:pdh-data → tag:pdh:8443`. Client meldet sich mit eigenem Token (im PDH gehasht).
5. **Ziele je Client:** Ein Client kann mehrere lokale Datenbanken anbieten (Liste fester Ports, Ziel nur `127.0.0.1` oder eine fest eingetragene Adresse, beim Client konfiguriert); das PDH kann keine anderen Ziele anfordern.
6. **Sperren wirkt sofort:** Token ungültig, Knoten im Netz löschen (Headscale-API bzw. Tailscale-API, falls Schlüssel vorhanden), Freigabe entziehen. „Kein Kontakt seit 3 Takten“ = offline; Warnhinweis „Client veraltet seit X Tagen“.
7. **Windows:** eine EXE mit `install`/`uninstall`/`run` als Windows-Dienst (`x/sys/windows/svc`), Wiederherstellung „Neu starten“; Update: neue Datei `.new`, Dienst anhalten, alt → `.old`, neu → `.exe`, starten, nach 60 s Status prüfen, sonst zurück. Linux gleich mit systemd. Keine Windows-Codesignatur in Version 1 (Handbuch-Hinweis zu SmartScreen).
8. **Signatur:** Ed25519 über Hash, Version und Plattform; kein Rückspielen älterer Versionen ohne ausdrückliche Anweisung; zweiter Reserveschlüssel für den Wechsel.
9. **Cloud-Schlüssel:** zuerst Einmal-Schlüssel von Hand eintragen und die nötige Freigabe-Zeile anzeigen; automatisches Erzeugen optional über einen OAuth-Client nur mit Recht `auth_keys`, Ablauf im PDH sichtbar.
10. **Start nie blockierend:** Netze sind standardmäßig aus; Start im Hintergrund mit Zeitgrenze und Wiederholung; ein Ausfall betrifft nur Verbindungen mit Weg „Datennetz“ (Test: PDH ohne bzw. mit hängender Steuerung in unter 5 s bereit).
11. **Netz-Einzelheiten:** intern eingebauter DERP-Relay von Headscale, MagicDNS aus, Adressierung über die gespeicherte Datennetz-Adresse; Cloud mit den Tailscale-Standardrelays (Hinweis: Verbindungsdaten wie Gerätenamen liegen bei tailscale.com, Inhalte nicht).
12. **Rechte:** `datanet.view/edit` bekommen die Rollen mit den heutigen VPN-Rechten, `datanet.admin` die Bereichsadministratoren von VPN bzw. Verbindungen (im Plan per Code ermittelt); Clients ohne Abteilung (global).
13. **Auslieferung:** `cmd/pdh-data-client`, ohne CGO für Windows und Linux gebaut und signiert im selben Schritt wie das PDH, neben dem PDH ausgeliefert. Neue Abhängigkeit `tailscale.com` (BSD-3) wird auf Größe und Go-Version geprüft.
14. **Reihenfolge der Pakete:** 1 Client + Cloud-Netz + Weg und Brücke + Rückkanal; 2 Updates über das PDH; 3 Headscale intern (größte Unsicherheit zuletzt).
