# Microsoft 365

## Nutzerkonto verbinden

PDH kann ein Microsoft-Geschäfts- oder Privatkonto pro angemeldetem Nutzer verknüpfen. Die Verknüpfung erfolgt unter **Mein Konto** und verwendet Microsoft OAuth mit PKCE. Die lokale PDH-Anmeldung bleibt bestehen.

Die Nutzerverknüpfung fordert `User.Read` und `Calendars.ReadWrite` als delegierte Graph-Berechtigungen an. Access- und Refresh-Tokens werden mit AES-GCM verschlüsselt in PostgreSQL gespeichert; der Verschlüsselungsschlüssel wird aus `PDH_AUTH_JWTSECRET` abgeleitet. Eine Änderung dieses Geheimnisses macht bestehende Microsoft-Verknüpfungen unlesbar; betroffene Nutzer müssen ihr Konto erneut verbinden.

## Entra-App registrieren

1. In Microsoft Entra eine App-Registrierung erstellen.
2. Als unterstützte Kontotypen **Konten in beliebigen Organisationsverzeichnissen und persönliche Microsoft-Konten** wählen.
3. Eine Web-Redirect-URI eintragen, die exakt auf `https://pdh.strobl-home.net/account/microsoft/callback` zeigt. Bei abweichender PDH-Adresse diese stattdessen verwenden.
4. Einen Client-Secret-Wert erzeugen und die delegierten Microsoft Graph-Berechtigungen `User.Read`, `Calendars.ReadWrite`, `Chat.ReadWrite` und `ChannelMessage.Send` hinzufügen.
5. Für den Organisationsabgleich zusätzlich die Anwendungsberechtigung `User.Read.All` hinzufügen und Admin-Consent für den Tenant erteilen.
6. Client-ID, Secret und Redirect-URI auf dem Server als Umgebungsvariablen konfigurieren:

```env
PDH_MICROSOFT_CLIENT_ID=<Application (client) ID>
PDH_MICROSOFT_CLIENT_SECRET=<Client secret value>
PDH_MICROSOFT_REDIRECT_URL=https://pdh.strobl-home.net/account/microsoft/callback
PDH_MICROSOFT_TENANT_ID=<Entra tenant ID>
PDH_MICROSOFT_TEAMS_SENDER_USER_ID=<PDH user UUID for the Teams sender>
PDH_MICROSOFT_TEAMS_ID=<Teams team ID>
PDH_MICROSOFT_TEAMS_CHANNEL_ID=<Teams channel ID>
```

Für systemd stehen die Werte in der PDH-`EnvironmentFile` (üblicherweise `.env`). In Docker Compose gehören sie in `.env.docker`; danach den App-Container neu erstellen. Client-Secrets nicht in Git einchecken oder in Support-Chats senden.

Nach dem Neustart erscheint **Mein Konto** im Nutzermenü. Jeder Nutzer kann dort sein Microsoft-Konto verbinden oder trennen. Bei persönlichen Konten ohne `mail`-Attribut zeigt PDH den Anzeigenamen beziehungsweise die Microsoft-ID.

## Kalender, Verzeichnis und Teams

Jeder verbundene Nutzer kann unter **Mein Konto** auswählen, ob Schichten, zugewiesene Aufgaben, Wartungen, Tickets und Störungen bei „Jetzt synchronisieren“ in seinen Outlook-Kalender geschrieben werden. Outlook-Termine mit Status „busy“, „tentative“, „out of office“ oder „working elsewhere“ werden optional als private Zeitblöcke importiert. Betreffzeilen werden nicht gespeichert; die Blocker erscheinen als Warnmarkierung im PDH-Schichtwochenplan und verhindern keine manuelle Zuweisung. PDH bleibt für seine betrieblichen Datensätze führend.

Administratoren gleichen unter **Core-Einstellungen → Microsoft-365-Organisationssync** den konfigurierten Entra-Tenant ab. Der Abgleich ordnet nur bestehende aktive PDH-Nutzer anhand der E-Mail-Adresse zu; er legt niemanden an, ändert keine Rollen und deaktiviert keine Konten.

Für Teams muss ein Geschäfts-/Schulkonto unter **Mein Konto → Teams freigeben** die zusätzlichen delegierten Rechte erteilen. `PDH_MICROSOFT_TEAMS_SENDER_USER_ID` bezeichnet das PDH-Konto, dessen verbundenes Geschäftskonto Nachrichten versendet. Team- und Channel-ID richten den Kanalversand ein. Benachrichtigungen werden derzeit bei Aktionen im globalen PDH-Leitstand ausgelöst. Persönliche Microsoft-Konten unterstützen diese Teams-Funktionen nicht.

## Grenzen

Der Kalenderabgleich ist nutzergesteuert und wird manuell gestartet; es gibt noch keinen automatischen Hintergrund-Sync. Der Entra-Abgleich gilt nur für den konfigurierten Geschäftstenant. Private Microsoft-Konten können ihr Konto und ihren Outlook-Kalender verbinden, aber weder ein Organisationsverzeichnis synchronisieren noch Teams-Nachrichten versenden.