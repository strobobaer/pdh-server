# MQTT-Name sicher einrichten (0.82.15) – kurzer Entwurf

Vom Anwender am 21.11.2026 bestätigt („Passt so“).

## Anlass

Auf 10.10.0.202 wurden am 21.11.2026 um 12:36 die Ports des eingebauten
Brokers geändert und der Name `mqtt.strobl-home.net` eingerichtet. Danach war
das PDH über seinen Namen nicht mehr erreichbar (Cloudflare 502):

1. Der Update-Agent schrieb die nginx-Seite für den MQTT-Namen mit
   `listen [::]:443 ssl;`. Die von Hand angelegte Seite `pdh` hört nur auf
   IPv4. Der Tunnel ruft `https://localhost:443` (strenger Modus,
   `certStrictService`) auf, `localhost` ist dort zuerst `::1` – über IPv6 gab
   es nur die MQTT-Seite, also kam deren Zertifikat, der Tunnel lehnte es ab.
2. Die Tunnel-Zeile für den MQTT-Namen zeigte weiter auf den alten
   WebSocket-Port (`http://localhost:8083`), der Broker hörte auf 8093.

Wunsch des Anwenders: „bitte sicher gestalten, dass eine Eingabe eines Namens
zwingend die dazugehörigen DNS-Einträge vornimmt oder vornehmen lässt“.

## Ziele

- Ein neuer oder geänderter Name stört nie einen anderen Namen am Server.
- Name und Ports wirken nur als Ganzes: Zertifikat, nginx-Seite,
  Cloudflare-DNS und Tunnel-Zeile, Eintrag im Haus (UniFi), Firewall.
- Was das PDH selbst darf, erledigt es sofort; was nicht, zeigt es als Liste
  mit den genauen Einträgen.
- **Scharf erst, wenn alles grün ist:** Bis dahin bleibt am Server alles wie
  vorher (keine neue nginx-Seite, keine geänderte Tunnel-Zeile).
- Eine Port-Änderung zieht die Tunnel-Zeile und die nginx-Seite automatisch
  nach.

## Nicht-Ziele

- Von Hand angelegte nginx-Seiten (z. B. `pdh`) ändert das PDH nicht.
- Kein neues Recht: Es gelten die Rechte der bestehenden Knöpfe (Broker-Seite,
  Cloudflare-Reiter, UniFi).
- Keine Änderung an Stufen ohne Namen (Broker nur mit Adresse bleibt wie heute).

## Anti-Ziele

- Ein Fehler beim Einrichten darf nie den Zugang zum PDH kosten – lieber gar
  nicht scharf schalten als halb.

## Ansätze

- **A – Nur Vorlagen reparieren** (IPv6-Zeile weglassen, `127.0.0.1` im
  Tunnel): behebt diesen Fall, schützt aber nicht vor dem nächsten ähnlichen.
- **B – Prüfen und Zurücknehmen im Update-Agenten plus Gesamtablauf im PDH**
  (gewählt): Der Agent vergleicht vor und nach dem Neuladen, welches
  Zertifikat jeder andere Name bekommt, und nimmt die neue Seite bei einer
  Änderung zurück; das PDH schaltet den Namen erst scharf, wenn alle Einträge
  stehen.
- **C – nginx ganz vom PDH verwalten**: verworfen, die Seiten des Anwenders
  bleiben seine.

## Festlegungen

1. **Kein Name stört einen anderen.** Vor dem Schreiben einer Seite liest der
   Agent für alle anderen `server_name` auf Port 443 den Fingerabdruck des
   Zertifikats, das über `127.0.0.1` und `::1` mit diesem Namen (SNI) kommt.
   Nach `nginx -t` und Neuladen vergleicht er. Ändert sich ein Fingerabdruck
   (oder antwortet ein vorher erreichbarer Name nicht mehr), stellt er den
   alten Stand her, lädt neu und meldet „würde <Name> stören“.
2. **IPv6 nur, wenn die anderen es auch tun.** Eine neue Seite hört nur dann
   zusätzlich auf `[::]:443`/`[::]:80`, wenn jede andere Seite auf Port 443
   ebenfalls auf IPv6 hört.
3. **Eindeutige Tunnel-Ziele.** Das PDH trägt `https://127.0.0.1:443` und
   `http://127.0.0.1:<Port>` statt `localhost` ein. Vorhandene Zeilen mit
   `localhost` gelten weiter als richtig (gleiche Bedeutung) und werden beim
   nächsten Schreiben umgestellt.
4. **Gesamtablauf beim Speichern von Name oder Ports:** Das PDH legt einen
   Auftrag mit allen Schritten an (Zertifikat, Cloudflare-DNS, Tunnel-Zeile,
   Eintrag im Haus, Firewall, nginx-Seite). Schritte, die es selbst darf,
   führt es aus; die übrigen stehen als Liste mit dem genauen Eintrag
   (Typ, Name, Ziel) und einem „Erneut prüfen“. Die nginx-Seite und die
   Tunnel-Zeile für den Namen werden erst geschrieben, wenn alle übrigen
   Punkte grün sind.
5. **Port-Änderung:** Ändert sich der WebSocket-Port bei aktivem Namen, setzt
   das PDH die Tunnel-Zeile und das Ziel der nginx-Seite im selben Ablauf neu.
6. Handbuch (Kapitel zum Broker-Namen und Cloudflare-Tunnel) und
   CHANGELOG (`### Behoben`, `### Geändert`) nachziehen.

## Dem Bauenden überlassen

- Wie der Auftrag gespeichert wird (eigene Tabelle oder vorhandene Spalten).
- Zeitlimits für die Prüfung der Zertifikate (kurz, je Name höchstens wenige
  Sekunden).
- Genaue Texte der Liste, im Stil der bestehenden Prüfliste.
