# Nextcloud mit Microsoft 365 verbinden

Diese Anleitung richtet ein, dass sich alle Mitarbeitenden **mit ihrem Microsoft-365-Konto bei Nextcloud anmelden**. Das PDH legt dazu die Nextcloud-Konten an. Am Ende gilt:

- Auf der Nextcloud-Anmeldeseite (`https://cloud.strobl-home.net`) gibt es den Knopf **„Mit Microsoft anmelden“**.
- Wer im PDH einem Microsoft-Konto zugeordnet ist, bekommt beim Microsoft-Abgleich automatisch ein Nextcloud-Konto. Die Benutzer-ID ist die Microsoft-E-Mail.
- In Nextcloud kommt nur hinein, wer dort ein Konto hat. **Das PDH entscheidet also, wer Nextcloud bekommt.** Ein beliebiges Konto aus dem Tenant reicht nicht.

Zeitaufwand: etwa 30 Minuten. Die Schritte bauen aufeinander auf; bitte der Reihe nach vorgehen.

---

## So funktioniert es

```
Microsoft Entra ID ──(Verzeichnisabgleich)──► PDH ──(Konto anlegen, ID = E-Mail)──► Nextcloud
        ▲                                                                              │
        └────────────────(Anmeldung „Mit Microsoft anmelden“, OpenID Connect)──────────┘
```

1. Das PDH gleicht das Microsoft-Verzeichnis ab und ordnet die Personen über die E-Mail-Adresse zu.
2. Für jede zugeordnete Person ohne Nextcloud-Konto legt das PDH eines an. Die Nextcloud-Benutzer-ID ist die Microsoft-E-Mail, z. B. `anton.proebstl@firma.de`.
3. Bei der Anmeldung schickt Nextcloud die Person zu Microsoft. Microsoft meldet die E-Mail zurück, und Nextcloud öffnet das Konto mit genau dieser ID.

Die drei Teile hängen an einer Stelle zusammen: **Die E-Mail aus Microsoft muss genau der Nextcloud-Benutzer-ID entsprechen.** Fast alle Fehler weiter unten haben hier ihre Ursache.

---

## Voraussetzungen

| Was | Wo prüfen |
|---|---|
| PDH ab Version **0.52.0** läuft (Kopfzeile oder `/health`) | PDH |
| **Server-Einstellungen → Microsoft 365**: Tenant-ID, Client-ID und Client-Secret eingetragen, Punkt am Reiter grün | PDH |
| **Server-Einstellungen → Nextcloud**: Adresse und ein Nextcloud-Benutzer **mit Admin-Rechten** eingetragen, Punkt grün | PDH |
| Zugang zum **Microsoft Entra Admin Center** als Anwendungs- oder globaler Administrator | entra.microsoft.com |
| **Root-Zugang** (`sudo`) auf dem Server, auf dem Nextcloud läuft (Standard: `/var/www/nextcloud`) | Server |
| Das PDH-Repository liegt auf diesem Server (dort steht das Skript `scripts/configure-nextcloud-microsoft-login.sh`) | Server |

Notiere dir vorab:

- **Tenant-ID** und **Client-ID**: Entra → App-Registrierungen → PDH-App → Übersicht.
- Den **Client-Secret-Wert**. Er wird nur beim Anlegen angezeigt. Falls er nicht mehr vorliegt: Entra → PDH-App → Zertifikate & Geheimnisse → neues Geheimnis anlegen. Es danach auch im PDH unter Server-Einstellungen → Microsoft 365 eintragen.
- Die genaue **Nextcloud-Adresse**, z. B. `https://cloud.strobl-home.net`.

---

## Schritt 1 – Entra: App-Registrierung vorbereiten

Wir verwenden die vorhandene App-Registrierung des PDH. Eine zweite App ist nicht nötig.

### 1a. Redirect-URI für Nextcloud eintragen

1. [entra.microsoft.com](https://entra.microsoft.com) → **Identität → Anwendungen → App-Registrierungen** → die PDH-App öffnen.
2. **Authentifizierung** → bei der Plattform **Web** auf **URI hinzufügen**.
3. Eintragen:
   ```
   https://cloud.strobl-home.net/apps/user_oidc/code
   ```
   Hat Nextcloud keine „schönen URLs“ (die Adressen enthalten `/index.php/`), zusätzlich:
   ```
   https://cloud.strobl-home.net/index.php/apps/user_oidc/code
   ```
4. **Speichern**.

Die vorhandene PDH-Redirect-URI (`…/account/microsoft/callback`) bleibt unverändert.

### 1b. E-Mail in das Anmelde-Token aufnehmen

Ohne diesen Schritt schickt Microsoft keine E-Mail mit. Nextcloud findet das Konto dann nicht.

1. In der PDH-App: **Tokenkonfiguration → Optionalen Anspruch hinzufügen**.
2. Tokentyp **ID** wählen, Anspruch **email** anhaken → **Hinzufügen**.
3. Fragt Entra, ob die Microsoft-Graph-Berechtigung **email** eingeschaltet werden soll: **ja** anhaken, **Hinzufügen**.

### 1c. Berechtigungen prüfen

**API-Berechtigungen** → unter **Microsoft Graph (delegiert)** müssen stehen:

- `openid`
- `profile`
- `email`
- `User.Read` (hat das PDH schon)

Fehlt eine: **Berechtigung hinzufügen → Microsoft Graph → Delegierte Berechtigungen** → auswählen. Danach **Administratorzustimmung für … erteilen** und bestätigen. Bei allen Einträgen steht dann ein grüner Haken.

Für das Anlegen der Nextcloud-Konten braucht die App außerdem die **Anwendungsberechtigung** `User.Read.All` oder `Directory.Read.All` für den Verzeichnisabgleich. Sie ist vorhanden, wenn der Abgleich unter Core-Einstellungen → Microsoft 365 schon funktioniert.

---

## Schritt 2 – Nextcloud: Anmeldung mit Microsoft einrichten

Auf dem Nextcloud-Server im PDH-Verzeichnis (z. B. `cd /opt/pdh` – dort, wo das Repository liegt):

### Fall A – systemd-Installation (Zugangsdaten in `/etc/pdh/pdh.env`)

```bash
sudo bash scripts/configure-nextcloud-microsoft-login.sh
```

Das Skript liest Tenant-ID, Client-ID und Secret selbst aus `/etc/pdh/pdh.env`.

### Fall B – Docker-Installation oder Zugangsdaten nur in der PDH-Datenbank

Seit „In die Datenbank übernehmen“ stehen die Werte nicht mehr in einer Datei. Dann gibst du sie mit. Das Secret übergibst du als Umgebungsvariable, damit es nicht in der Shell-Historie landet; das Leerzeichen am Zeilenanfang verhindert das bei den meisten Shells zusätzlich:

```bash
 sudo ENTRA_CLIENT_SECRET='HIER-DAS-SECRET' bash scripts/configure-nextcloud-microsoft-login.sh \
     --tenant-id 00000000-0000-0000-0000-000000000000 \
     --client-id 11111111-1111-1111-1111-111111111111
```

### Was das Skript macht

1. Es installiert und aktiviert die Nextcloud-App **OpenID Connect user backend** (`user_oidc`).
2. Es legt den Anbieter **„Microsoft“** an: Entra-Tenant als Aussteller, Benutzer-ID = E-Mail (`--mapping-uid=email`, `--unique-uid=0`), Anzeigename und E-Mail aus Microsoft.
3. Es setzt `auto_provision = false`: Nextcloud legt bei der Anmeldung **keine** neuen Konten an. Wer kein Konto hat, kommt nicht hinein.
4. Es lässt die normale Passwort-Anmeldung bestehen.
5. Am Ende gibt es die Redirect-URI aus. Vergleiche sie mit Schritt 1a.

### Optionen

| Option | Wirkung |
|---|---|
| `--provider NAME` | Beschriftung des Knopfs (Standard: `Microsoft`) |
| `--auto-provision` | Jede Person aus dem Tenant bekommt bei der ersten Anmeldung ein Konto – **das PDH entscheidet dann nicht mehr**. Nur verwenden, wenn das gewollt ist. |
| `--only-microsoft` | Passwortfeld auf der Anmeldeseite ausblenden. Erst einschalten, wenn alles läuft (siehe Schritt 5). |
| `--nextcloud-dir PFAD` | Nextcloud liegt nicht in `/var/www/nextcloud` |
| `--env-file PFAD` | andere Datei mit den PDH-Zugangsdaten |

Das Skript lässt sich gefahrlos erneut ausführen, z. B. nach einem neuen Client-Secret.

---

## Schritt 3 – PDH: Nextcloud-Konten anlegen lassen

1. Im PDH als Administrator: **Core-Einstellungen → Microsoft-365-Organisationssync**.
2. **Jetzt abgleichen**. Die Meldung oben zeigt, wie viele Personen zugeordnet wurden.
3. Die Liste **Manuelle Zuordnung** prüfen. Steht bei jemandem „Nicht zugeordnet“, weicht die E-Mail im PDH von der in Microsoft ab:
   - entweder die E-Mail im Benutzerstamm korrigieren und erneut abgleichen,
   - oder über das Stift-Symbol die Microsoft-Objekt-ID von Hand zuordnen.
4. Karte **Nextcloud** → **„Beim Abgleich fehlende Nextcloud-Konten anlegen“** anhaken.
5. Noch einmal **Jetzt abgleichen**. Die Meldung endet jetzt mit z. B. „Nextcloud: 12 Konten verknüpft“.

Steht dort „… nicht möglich (Server-Protokoll)“, findest du die betroffenen Personen unter **Server-Einstellungen → Protokoll**, Bereich `nextcloud`. Häufige Gründe stehen unter „Fehlersuche“.

**Wer bekommt ein Konto?** Nur Personen, die
- im PDH aktiv sind,
- einem Microsoft-Konto zugeordnet sind, das in Microsoft aktiv ist,
- noch **kein** Nextcloud-Konto im PDH eingetragen haben (Benutzerstamm → Stammdaten → Nextcloud-Benutzer).

Gastkonten aus Microsoft (`…#EXT#…`) bekommen kein Konto.

Später angelegte Personen bekommen ihr Konto beim nächsten **Jetzt abgleichen**. Alternativ im Benutzerstamm der Person über **Nextcloud-Konto anlegen**; der Knopf nimmt dann ebenfalls die Microsoft-E-Mail als ID.

---

## Schritt 4 – Testen

1. Ein **privates Browserfenster** öffnen, damit keine alte Anmeldung stört.
2. `https://cloud.strobl-home.net` aufrufen → **Mit Microsoft anmelden**.
3. Mit dem Microsoft-Konto einer Person anmelden, die in Schritt 3 ein Konto bekommen hat. Beim ersten Mal fragt Microsoft eventuell nach Zustimmung → **Akzeptieren**.
4. Du landest in Nextcloud, angemeldet als diese Person.
5. Gegenprobe: Mit einem Microsoft-Konto **ohne** Nextcloud-Konto anmelden → Nextcloud lehnt ab. So soll es sein.

In Nextcloud unter **Benutzer** (als Admin) siehst du die neuen Konten. Die Benutzer-ID ist jeweils die E-Mail-Adresse.

---

## Schritt 5 – Optional: nur noch Anmeldung mit Microsoft

Erst, wenn Schritt 4 für mehrere Personen geklappt hat:

```bash
sudo bash scripts/configure-nextcloud-microsoft-login.sh --only-microsoft
```

(Bei Fall B wieder mit `ENTRA_CLIENT_SECRET`, `--tenant-id` und `--client-id`.)

Danach zeigt die Anmeldeseite nur noch den Microsoft-Knopf. **Notzugang für Administratoren:** `https://cloud.strobl-home.net/login?direct=1` zeigt weiterhin das Passwortfeld. Das PDH selbst spricht Nextcloud über den Admin-Zugang aus den Server-Einstellungen an; das ist davon nicht betroffen.

---

## Wichtig: Personen, die schon ein Nextcloud-Konto haben

Konten, die **vor** dieser Umstellung angelegt wurden, haben meist den Benutzernamen als ID (z. B. `toni`), nicht die E-Mail. Für sie gilt:

- Das PDH lässt diese Konten unverändert. Es legt kein zweites Konto an, solange im Benutzerstamm ein Nextcloud-Benutzer eingetragen ist.
- **Die Anmeldung mit Microsoft findet diese Konten nicht**, weil die ID nicht zur E-Mail passt. Die Personen melden sich weiter mit Benutzername und Passwort an. Deshalb Schritt 5 erst, wenn das geklärt ist.
- Nextcloud kann eine Benutzer-ID nicht umbenennen. Wer auf Microsoft umsteigen soll, braucht ein neues Konto:
  1. Daten sichern bzw. an das neue Konto freigeben (Dateien, Kalender, Kontakte).
  2. Im PDH-Benutzerstamm das Feld **Nextcloud-Benutzer** leeren und speichern.
  3. **Jetzt abgleichen** (oder **Nextcloud-Konto anlegen**) → neues Konto mit der E-Mail als ID.
  4. Altes Konto in Nextcloud deaktivieren, sobald alles übertragen ist.

---

## Fehlersuche

| Meldung / Verhalten | Ursache | Lösung |
|---|---|---|
| Microsoft: **AADSTS50011** „redirect URI … does not match“ | Redirect-URI fehlt oder ist falsch geschrieben | Schritt 1a: genau die Adresse aus der letzten Ausgabe des Skripts eintragen (mit oder ohne `/index.php/`) |
| Microsoft: **AADSTS700016** „application … not found in the directory“ | falsche Client-ID oder Tenant-ID | Werte in Entra → Übersicht prüfen, Skript mit den richtigen Werten erneut ausführen |
| Microsoft: **AADSTS7000215** „invalid client secret“ | Secret abgelaufen oder falsch (die **Geheimnis-ID** statt des **Werts** kopiert) | neues Geheimnis anlegen, den **Wert** verwenden, Skript erneut ausführen und im PDH eintragen |
| Microsoft: **AADSTS65001** „consent“ | Administratorzustimmung fehlt | Schritt 1c: „Administratorzustimmung erteilen“ |
| Nach Microsoft-Anmeldung: Nextcloud lehnt ab, obwohl das Konto existiert | Microsoft schickt keine oder eine andere E-Mail | Schritt 1b (optionaler Anspruch **email**) prüfen. In Entra beim Benutzer muss **E-Mail** gefüllt sein und genau der Nextcloud-ID entsprechen. |
| Nach Microsoft-Anmeldung: Nextcloud lehnt ab, Person hat kein Konto | gewollt (`auto_provision=false`) | Person im PDH zuordnen und **Jetzt abgleichen** |
| PDH: „Nextcloud lehnt den Zugang ab – … braucht Admin-Rechte“ | der Nextcloud-Benutzer in den Server-Einstellungen ist kein Admin | in Nextcloud zur Gruppe `admin` hinzufügen |
| PDH-Protokoll: „… taugt nicht als Nextcloud-Benutzer-ID“ | E-Mail enthält Umlaute oder Sonderzeichen, oder es ist ein Gastkonto | E-Mail in Microsoft anpassen oder Konto von Hand anlegen |
| PDH-Protokoll: „ist schon einer anderen Person zugeordnet“ | zwei PDH-Benutzer mit derselben E-Mail | doppelten Benutzer im PDH bereinigen |
| Knopf „Mit Microsoft anmelden“ fehlt | App `user_oidc` nicht aktiv | `sudo -u www-data php /var/www/nextcloud/occ app:list \| grep user_oidc`, Skript erneut ausführen |
| Kontrollen auf dem Server | | `sudo -u www-data php /var/www/nextcloud/occ user_oidc:provider` zeigt den Anbieter; `… occ config:system:get user_oidc` zeigt `auto_provision` |

---

## Zurücknehmen

- **PDH:** Core-Einstellungen → Microsoft 365 → Karte Nextcloud → Haken entfernen. Bereits angelegte Konten bleiben.
- **Nextcloud** (auf dem Server):
  ```bash
  sudo -u www-data php /var/www/nextcloud/occ user_oidc:provider:delete Microsoft
  sudo -u www-data php /var/www/nextcloud/occ config:app:set user_oidc allow_multiple_user_backends --value=1
  ```
  Danach ist die Passwort-Anmeldung wieder die einzige. Personen ohne Passwort setzen es über „Passwort vergessen“ oder ein Admin in Nextcloud.
- **Entra:** Redirect-URI `…/apps/user_oidc/code` und den optionalen Anspruch können bleiben oder entfernt werden.

---

## Technische Einzelheiten

- Nextcloud-App: [user_oidc](https://apps.nextcloud.com/apps/user_oidc). Discovery-URL: `https://login.microsoftonline.com/<Tenant-ID>/v2.0/.well-known/openid-configuration`, Scope `openid email profile`.
- Konto-Regeln in `config.php` (gesetzt per `occ config:system:set`): `user_oidc.auto_provision = false`, `user_oidc.soft_auto_provision = true` (vorhandene Konten mit passender ID werden übernommen).
- PDH-Seite: Einstellung `microsoft_nextcloud_provision` (`app_settings`). Code: `internal/web/microsoft_directory_sync.go` (`provisionNextcloudFromMicrosoft`), `internal/web/nextcloud_account.go`, `internal/integrations/nextcloud/users.go` (Provisioning-API `POST /ocs/v1.php/cloud/users`).
- Skript: `scripts/configure-nextcloud-microsoft-login.sh` (gibt das Secret nicht aus).
