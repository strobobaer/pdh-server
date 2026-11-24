# Lizenzen der Fremdbestandteile

PDH selbst steht unter der **GNU Affero General Public License, Version 3 oder
später** (AGPL-3.0-or-later) – siehe [LICENSE](LICENSE), [COPYING.md](COPYING.md)
und die Ausnahme für erzeugte Ausgaben in
[LICENSE-EXCEPTION.md](LICENSE-EXCEPTION.md).

PDH nutzt Bausteine anderer Projekte. Sie behalten ihre eigene Lizenz. Die
Lizenzen der Bausteine, die PDH mitliefert (Abschnitte 1 und 2), sind mit der
AGPL vereinbar. Die Lizenztexte und Hinweisdateien (NOTICE) liegen in
[third_party/notices/](third_party/notices/).

## 1. Go-Module (im Programm enthalten)

Alle Module aus `go.mod`, direkt und indirekt. Manche werden nur für ein
bestimmtes Betriebssystem eingebunden (z. B. Windows für den Datenclient).

So entsteht die Tabelle: `go run ./scripts/thirdpartylicenses` liest `go.mod`,
bestimmt die Lizenz aus den Lizenzdateien im Go-Modul-Cache und schreibt die
Tabelle unten neu. Dasselbe Programm legt alle Lizenztexte in
[third_party/notices/go-modules-licenses.txt](third_party/notices/go-modules-licenses.txt)
ab. Nach jeder Änderung an `go.mod` erneut ausführen (vorher `go mod download`).

Besonderheiten:

- **Eclipse Paho MQTT** (`github.com/eclipse/paho.mqtt.golang`) steht wahlweise
  unter EPL-2.0 oder EDL-1.0 (Eclipse Distribution License 1.0, inhaltlich eine
  BSD-3-Clause-Lizenz). PDH nutzt es unter **EDL-1.0**. Lizenz, `edl-v10` und
  NOTICE liegen in
  [third_party/notices/github.com/eclipse/paho.mqtt.golang/](third_party/notices/github.com/eclipse/paho.mqtt.golang/).
- **MPL-2.0** (`github.com/go-sql-driver/mysql`, `github.com/hashicorp/golang-lru/v2`):
  Die Mozilla Public License gilt **je Datei**. PDH verändert diese Dateien nicht.
  Ihr Quellcode liegt unter der genannten Quelle und im Go-Modul-Cache
  (`go mod download`). Wer Dateien dieser Module ändert, muss die geänderten
  Dateien unter MPL-2.0 weitergeben; der übrige Code von PDH bleibt davon
  unberührt.
- **YAML** (`gopkg.in/yaml.v3`, `go.yaml.in/yaml/v3`, `go.yaml.in/yaml/v4`):
  MIT und Apache-2.0. Ihre NOTICE-Dateien liegen in `third_party/notices/`
  unter dem Namen des Moduls.
- **Apache-2.0**: Bringt ein Modul eine NOTICE-Datei mit, liegt sie ebenfalls in
  `third_party/notices/` unter dem Namen des Moduls.

<!-- go-module-table:start (go run ./scripts/thirdpartylicenses) -->

| Modul | Version | Art | Lizenz | Quelle |
| --- | --- | --- | --- | --- |
| `github.com/anthropics/anthropic-sdk-go` | v1.78.0 | direkt | MIT | <https://github.com/anthropics/anthropic-sdk-go> |
| `github.com/eclipse/paho.mqtt.golang` | v1.5.1 | direkt | EDL-1.0 (gewählt; alternativ EPL-2.0) | <https://github.com/eclipse/paho.mqtt.golang> |
| `github.com/emersion/go-sasl` | v0.0.0-20200509203442-7bfe0ed36a21 | direkt | MIT | <https://github.com/emersion/go-sasl> |
| `github.com/emersion/go-smtp` | v0.21.3 | direkt | MIT | <https://github.com/emersion/go-smtp> |
| `github.com/fsnotify/fsnotify` | v1.10.1 | direkt | BSD-3-Clause | <https://github.com/fsnotify/fsnotify> |
| `github.com/go-chi/chi/v5` | v5.1.0 | direkt | MIT | <https://github.com/go-chi/chi> |
| `github.com/go-pdf/fpdf` | v0.9.0 | direkt | MIT | <https://github.com/go-pdf/fpdf> |
| `github.com/go-sql-driver/mysql` | v1.8.1 | direkt | MPL-2.0 | <https://github.com/go-sql-driver/mysql> |
| `github.com/golang-jwt/jwt/v5` | v5.2.1 | direkt | MIT | <https://github.com/golang-jwt/jwt> |
| `github.com/google/uuid` | v1.6.0 | direkt | BSD-3-Clause | <https://github.com/google/uuid> |
| `github.com/gopcua/opcua` | v0.9.1 | direkt | MIT | <https://github.com/gopcua/opcua> |
| `github.com/gorilla/websocket` | v1.5.4-0.20250319132907-e064f32e3674 | direkt | BSD-2-Clause | <https://github.com/gorilla/websocket> |
| `github.com/gosnmp/gosnmp` | v1.47.0 | direkt | BSD-2-Clause + BSD-3-Clause (Teile von Go) | <https://github.com/gosnmp/gosnmp> |
| `github.com/jackc/pgx/v5` | v5.6.0 | direkt | MIT | <https://github.com/jackc/pgx> |
| `github.com/jlaffaye/ftp` | v0.2.4 | direkt | ISC | <https://github.com/jlaffaye/ftp> |
| `github.com/microsoft/go-mssqldb` | v1.7.2 | direkt | BSD-3-Clause | <https://github.com/microsoft/go-mssqldb> |
| `github.com/mochi-mqtt/server/v2` | v2.7.9 | direkt | MIT | <https://github.com/mochi-mqtt/server> |
| `github.com/pkg/sftp` | v1.13.12 | direkt | BSD-2-Clause | <https://github.com/pkg/sftp> |
| `github.com/robfig/cron/v3` | v3.0.1 | direkt | MIT | <https://github.com/robfig/cron> |
| `github.com/rs/zerolog` | v1.34.0 | direkt | MIT | <https://github.com/rs/zerolog> |
| `github.com/simonvetter/modbus` | v1.6.4 | direkt | MIT | <https://github.com/simonvetter/modbus> |
| `github.com/spf13/viper` | v1.21.0 | direkt | MIT | <https://github.com/spf13/viper> |
| `github.com/tailscale/hujson` | v0.0.0-20260727124030-b80ff77dac4f | direkt | BSD-3-Clause | <https://github.com/tailscale/hujson> |
| `github.com/xuri/excelize/v2` | v2.8.0 | direkt | BSD-3-Clause | <https://github.com/xuri/excelize> |
| `golang.org/x/crypto` | v0.58.0 | direkt | BSD-3-Clause | <https://pkg.go.dev/golang.org/x/crypto> |
| `golang.org/x/image` | v0.46.0 | direkt | BSD-3-Clause | <https://pkg.go.dev/golang.org/x/image> |
| `golang.org/x/net` | v0.60.0 | direkt | BSD-3-Clause | <https://pkg.go.dev/golang.org/x/net> |
| `golang.org/x/sys` | v0.49.0 | direkt | BSD-3-Clause | <https://pkg.go.dev/golang.org/x/sys> |
| `golang.org/x/text` | v0.44.0 | direkt | BSD-3-Clause | <https://pkg.go.dev/golang.org/x/text> |
| `gopkg.in/yaml.v3` | v3.0.1 | direkt | Apache-2.0 + MIT | <https://pkg.go.dev/gopkg.in/yaml.v3> |
| `modernc.org/sqlite` | v1.34.1 | direkt | BSD-3-Clause | <https://pkg.go.dev/modernc.org/sqlite> |
| `rsc.io/qr` | v0.2.0 | direkt | BSD-3-Clause | <https://pkg.go.dev/rsc.io/qr> |
| `software.sslmate.com/src/go-pkcs12` | v0.7.3 | direkt | BSD-3-Clause | <https://pkg.go.dev/software.sslmate.com/src/go-pkcs12> |
| `tailscale.com` | v1.104.7 | direkt | BSD-3-Clause | <https://pkg.go.dev/tailscale.com> |
| `filippo.io/edwards25519` | v1.2.0 | indirekt | BSD-3-Clause | <https://pkg.go.dev/filippo.io/edwards25519> |
| `github.com/akutz/memconn` | v0.1.0 | indirekt | Apache-2.0 | <https://github.com/akutz/memconn> |
| `github.com/alexbrainman/sspi` | v0.0.0-20250919150558-7d374ff0d59e | indirekt | BSD-3-Clause | <https://github.com/alexbrainman/sspi> |
| `github.com/bahlo/generic-list-go` | v0.2.0 | indirekt | BSD-3-Clause | <https://github.com/bahlo/generic-list-go> |
| `github.com/buger/jsonparser` | v1.1.2 | indirekt | MIT | <https://github.com/buger/jsonparser> |
| `github.com/coder/websocket` | v1.8.15 | indirekt | ISC | <https://github.com/coder/websocket> |
| `github.com/creachadair/msync` | v0.10.1 | indirekt | BSD-3-Clause | <https://github.com/creachadair/msync> |
| `github.com/dblohm7/wingoes` | v0.0.0-20260526185140-fb298caac7ca | indirekt | BSD-3-Clause | <https://github.com/dblohm7/wingoes> |
| `github.com/dustin/go-humanize` | v1.0.1 | indirekt | MIT | <https://github.com/dustin/go-humanize> |
| `github.com/fxamacker/cbor/v2` | v2.9.3 | indirekt | MIT | <https://github.com/fxamacker/cbor> |
| `github.com/gaissmai/bart` | v0.29.0 | indirekt | MIT | <https://github.com/gaissmai/bart> |
| `github.com/go-json-experiment/json` | v0.0.0-20260820222146-c27c302e5fc3 | indirekt | BSD-3-Clause | <https://github.com/go-json-experiment/json> |
| `github.com/go-viper/mapstructure/v2` | v2.5.0 | indirekt | MIT | <https://github.com/go-viper/mapstructure> |
| `github.com/goburrow/serial` | v0.1.0 | indirekt | MIT | <https://github.com/goburrow/serial> |
| `github.com/godbus/dbus/v5` | v5.2.2 | indirekt | BSD-2-Clause | <https://github.com/godbus/dbus> |
| `github.com/golang-sql/civil` | v0.0.0-20220223132316-b832511892a9 | indirekt | Apache-2.0 | <https://github.com/golang-sql/civil> |
| `github.com/golang-sql/sqlexp` | v0.1.0 | indirekt | BSD-3-Clause | <https://github.com/golang-sql/sqlexp> |
| `github.com/golang/groupcache` | v0.0.0-20241129210726-2c02b8208cf8 | indirekt | Apache-2.0 | <https://github.com/golang/groupcache> |
| `github.com/google/btree` | v1.1.3 | indirekt | Apache-2.0 | <https://github.com/google/btree> |
| `github.com/google/go-cmp` | v0.7.0 | indirekt | BSD-3-Clause | <https://github.com/google/go-cmp> |
| `github.com/hashicorp/golang-lru/v2` | v2.0.7 | indirekt | MPL-2.0 | <https://github.com/hashicorp/golang-lru> |
| `github.com/hdevalence/ed25519consensus` | v0.2.0 | indirekt | BSD-3-Clause | <https://github.com/hdevalence/ed25519consensus> |
| `github.com/huin/goupnp` | v1.3.0 | indirekt | BSD-2-Clause | <https://github.com/huin/goupnp> |
| `github.com/invopop/jsonschema` | v0.14.0 | indirekt | MIT | <https://github.com/invopop/jsonschema> |
| `github.com/jackc/pgpassfile` | v1.0.0 | indirekt | MIT | <https://github.com/jackc/pgpassfile> |
| `github.com/jackc/pgservicefile` | v0.0.0-20221227161230-091c0ba34f0a | indirekt | MIT | <https://github.com/jackc/pgservicefile> |
| `github.com/jackc/puddle/v2` | v2.2.1 | indirekt | MIT | <https://github.com/jackc/puddle> |
| `github.com/jsimonetti/rtnetlink` | v1.4.2 | indirekt | MIT | <https://github.com/jsimonetti/rtnetlink> |
| `github.com/klauspost/compress` | v1.20.0 | indirekt | Apache-2.0 + MIT + BSD-3-Clause | <https://github.com/klauspost/compress> |
| `github.com/kr/fs` | v0.1.0 | indirekt | BSD-3-Clause | <https://github.com/kr/fs> |
| `github.com/mattn/go-colorable` | v0.1.15 | indirekt | MIT | <https://github.com/mattn/go-colorable> |
| `github.com/mattn/go-isatty` | v0.0.24 | indirekt | MIT | <https://github.com/mattn/go-isatty> |
| `github.com/mdlayher/netlink` | v1.11.2 | indirekt | MIT | <https://github.com/mdlayher/netlink> |
| `github.com/mdlayher/socket` | v0.7.0 | indirekt | MIT | <https://github.com/mdlayher/socket> |
| `github.com/mitchellh/go-ps` | v1.0.0 | indirekt | MIT | <https://github.com/mitchellh/go-ps> |
| `github.com/mohae/deepcopy` | v0.0.0-20170929034955-c48cc78d4826 | indirekt | MIT | <https://github.com/mohae/deepcopy> |
| `github.com/ncruces/go-strftime` | v0.1.9 | indirekt | MIT | <https://github.com/ncruces/go-strftime> |
| `github.com/pb33f/ordered-map/v2` | v2.3.1 | indirekt | Apache-2.0 | <https://github.com/pb33f/ordered-map> |
| `github.com/pelletier/go-toml/v2` | v2.4.3 | indirekt | MIT | <https://github.com/pelletier/go-toml> |
| `github.com/pires/go-proxyproto` | v0.15.0 | indirekt | Apache-2.0 | <https://github.com/pires/go-proxyproto> |
| `github.com/remyoudompheng/bigfft` | v0.0.0-20230129092748-24d4a6f8daec | indirekt | BSD-3-Clause | <https://github.com/remyoudompheng/bigfft> |
| `github.com/richardlehane/mscfb` | v1.0.4 | indirekt | Apache-2.0 | <https://github.com/richardlehane/mscfb> |
| `github.com/richardlehane/msoleps` | v1.0.3 | indirekt | Apache-2.0 | <https://github.com/richardlehane/msoleps> |
| `github.com/rs/xid` | v1.6.0 | indirekt | MIT | <https://github.com/rs/xid> |
| `github.com/safchain/ethtool` | v0.7.0 | indirekt | Apache-2.0 | <https://github.com/safchain/ethtool> |
| `github.com/sagikazarmark/locafero` | v0.11.0 | indirekt | MIT | <https://github.com/sagikazarmark/locafero> |
| `github.com/sourcegraph/conc` | v0.3.1-0.20240121214520-5f936abd7ae8 | indirekt | MIT | <https://github.com/sourcegraph/conc> |
| `github.com/spf13/afero` | v1.15.0 | indirekt | Apache-2.0 | <https://github.com/spf13/afero> |
| `github.com/spf13/cast` | v1.10.0 | indirekt | MIT | <https://github.com/spf13/cast> |
| `github.com/spf13/pflag` | v1.0.10 | indirekt | BSD-3-Clause | <https://github.com/spf13/pflag> |
| `github.com/standard-webhooks/standard-webhooks/libraries` | v0.0.1 | indirekt | MIT | <https://github.com/standard-webhooks/standard-webhooks> |
| `github.com/subosito/gotenv` | v1.6.0 | indirekt | MIT | <https://github.com/subosito/gotenv> |
| `github.com/tailscale/certstore` | v0.1.1-0.20260409135935-3638fb84b77d | indirekt | MIT | <https://github.com/tailscale/certstore> |
| `github.com/tailscale/go-winio` | v0.0.0-20231025203758-c4f33415bf55 | indirekt | MIT | <https://github.com/tailscale/go-winio> |
| `github.com/tailscale/peercred` | v0.0.0-20250107143737-35a0c7bd7edc | indirekt | BSD-3-Clause | <https://github.com/tailscale/peercred> |
| `github.com/tailscale/web-client-prebuilt` | v0.0.0-20260917222731-e0ed2d0d0fea | indirekt | BSD-3-Clause | <https://github.com/tailscale/web-client-prebuilt> |
| `github.com/tailscale/wireguard-go` | v0.0.0-20261106233718-9e0ab45c16ca | indirekt | MIT | <https://github.com/tailscale/wireguard-go> |
| `github.com/tidwall/gjson` | v1.18.0 | indirekt | MIT | <https://github.com/tidwall/gjson> |
| `github.com/tidwall/match` | v1.1.1 | indirekt | MIT | <https://github.com/tidwall/match> |
| `github.com/tidwall/pretty` | v1.2.1 | indirekt | MIT | <https://github.com/tidwall/pretty> |
| `github.com/tidwall/sjson` | v1.2.5 | indirekt | MIT | <https://github.com/tidwall/sjson> |
| `github.com/x448/float16` | v0.8.4 | indirekt | MIT | <https://github.com/x448/float16> |
| `github.com/xuri/efp` | v0.0.0-20230802181842-ad255f2331ca | indirekt | BSD-3-Clause | <https://github.com/xuri/efp> |
| `github.com/xuri/nfp` | v0.0.0-20230819163627-dc951e3ffe1a | indirekt | BSD-3-Clause | <https://github.com/xuri/nfp> |
| `go.yaml.in/yaml/v3` | v3.0.5 | indirekt | Apache-2.0 + MIT | <https://pkg.go.dev/go.yaml.in/yaml/v3> |
| `go.yaml.in/yaml/v4` | v4.0.0-rc.2 | indirekt | Apache-2.0 + MIT | <https://pkg.go.dev/go.yaml.in/yaml/v4> |
| `go4.org/mem` | v0.0.0-20240501181205-ae6ca9944745 | indirekt | Apache-2.0 | <https://pkg.go.dev/go4.org/mem> |
| `go4.org/netipx` | v0.0.0-20260823151212-3075585bcbeb | indirekt | BSD-3-Clause | <https://pkg.go.dev/go4.org/netipx> |
| `golang.org/x/exp` | v0.0.0-20260908205506-85c1c2202aba | indirekt | BSD-3-Clause | <https://pkg.go.dev/golang.org/x/exp> |
| `golang.org/x/oauth2` | v0.37.0 | indirekt | BSD-3-Clause | <https://pkg.go.dev/golang.org/x/oauth2> |
| `golang.org/x/sync` | v0.25.0 | indirekt | BSD-3-Clause | <https://pkg.go.dev/golang.org/x/sync> |
| `golang.org/x/term` | v0.47.0 | indirekt | BSD-3-Clause | <https://pkg.go.dev/golang.org/x/term> |
| `golang.org/x/time` | v0.16.0 | indirekt | BSD-3-Clause | <https://pkg.go.dev/golang.org/x/time> |
| `golang.zx2c4.com/wintun` | v0.0.0-20230126152724-0fa3db229ce2 | indirekt | MIT | <https://pkg.go.dev/golang.zx2c4.com/wintun> |
| `golang.zx2c4.com/wireguard/windows` | v1.0.1 | indirekt | MIT | <https://pkg.go.dev/golang.zx2c4.com/wireguard/windows> |
| `gvisor.dev/gvisor` | v0.0.0-20260915211658-a6f909f08a72 | indirekt | Apache-2.0 + MIT + BSD-3-Clause | <https://pkg.go.dev/gvisor.dev/gvisor> |
| `modernc.org/gc/v3` | v3.0.0-20240107210532-573471604cb6 | indirekt | BSD-3-Clause | <https://pkg.go.dev/modernc.org/gc/v3> |
| `modernc.org/libc` | v1.55.3 | indirekt | MIT + BSD-3-Clause | <https://pkg.go.dev/modernc.org/libc> |
| `modernc.org/mathutil` | v1.6.0 | indirekt | BSD-3-Clause | <https://pkg.go.dev/modernc.org/mathutil> |
| `modernc.org/memory` | v1.8.0 | indirekt | BSD-3-Clause | <https://pkg.go.dev/modernc.org/memory> |
| `modernc.org/strutil` | v1.2.0 | indirekt | BSD-3-Clause | <https://pkg.go.dev/modernc.org/strutil> |
| `modernc.org/token` | v1.1.0 | indirekt | BSD-3-Clause | <https://pkg.go.dev/modernc.org/token> |

<!-- go-module-table:end -->

## 2. Mitgelieferte Bausteine für den Browser (`web/static/vendor/`)

Diese Dateien liegen im Repository und werden mit PDH ausgeliefert.

| Baustein | Version | Lizenz | Lizenztext | Quelle |
| --- | --- | --- | --- | --- |
| ESP Web Tools (USB-Flash im Browser), darin @material/web, esptool-js, @improv-wifi/serial-sdk (Apache-2.0), lit (BSD-3-Clause), pako, atob-lite (MIT), tslib (0BSD) | 10.5.2 | Apache-2.0 | `web/static/vendor/esp-web-tools/LICENSE.txt`, `THIRD-PARTY-LICENSES.txt` | <https://github.com/esphome/esp-web-tools> |
| noVNC (VNC-Client für den HMI-Fernzugriff, gebündelt als `rfb.min.js`) | 1.6.0 | MPL-2.0 (Teile BSD und MIT) | `web/static/vendor/novnc/LICENSE.txt` | <https://github.com/novnc/noVNC/tree/v1.6.0> |
| Tabler Icons (Symbole als SVG, zur Laufzeit als Stildatei mit Masken) | 3.30.0 | MIT | `web/static/vendor/tabler-icons/LICENSE.txt` | <https://github.com/tabler/tabler-icons> |

noVNC: `rfb.min.js` ist aus den unveränderten Quellen gebündelt (Bauanleitung in
`web/static/vendor/novnc/README.md`). Die MPL-2.0 gilt je Datei; der Quellcode
liegt unter der genannten Quelle. Den vollen Text der MPL-2.0 enthält
`third_party/notices/go-modules-licenses.txt` (bei `github.com/go-sql-driver/mysql`)
und <https://www.mozilla.org/MPL/2.0/>.

## 3. Bibliotheken der Seiten (mitgeliefert, unter `/vendor/<Version>/…`)

Diese Dateien liegen unverändert im Repository und kommen vom PDH-Server, nicht
aus dem Internet. Die Adresse enthält die Programmversion (`vendorURL`).

| Baustein | Version | Lizenz | Lizenztext | Quelle | Verwendet in |
| --- | --- | --- | --- | --- | --- |
| htmx | 1.9.12 | 0BSD | `web/static/vendor/htmx/LICENSE.txt` | <https://www.npmjs.com/package/htmx.org/v/1.9.12> | allen Seiten |
| SortableJS | 1.15.2 | MIT | `web/static/vendor/sortablejs/LICENSE.txt` | <https://www.npmjs.com/package/sortablejs/v/1.15.2> | Navigation (Menü ordnen), Leitstände, Dashboard-Widgets |
| Chart.js | 4.4.0 | MIT | `web/static/vendor/chartjs/LICENSE.txt` | <https://cdnjs.com/libraries/Chart.js/4.4.0> | Zeiterfassung |
| Frappe Gantt | 0.6.1 | MIT | `web/static/vendor/frappe-gantt/LICENSE.txt` | <https://www.npmjs.com/package/frappe-gantt/v/0.6.1> | Dashboard, Leitstand-Dashboard, Projekt |
| mammoth | 1.8.0 | BSD-2-Clause | `web/static/vendor/mammoth/LICENSE.txt` | <https://www.npmjs.com/package/mammoth/v/1.8.0> | Dokumentansicht (Word) |
| SheetJS Community Edition (xlsx) | 0.18.5 | Apache-2.0 | `web/static/vendor/xlsx/LICENSE.txt` | <https://www.npmjs.com/package/xlsx/v/0.18.5> | Dokumentansicht (Excel) |

## 4. Schriften (Google Fonts)

Die Schriften lädt der Browser von fonts.googleapis.com, wenn sie als
Schriftart (Erscheinungsbild) gewählt sind (Standard: DM Sans). PDH liefert sie
nicht mit.

| Schrift | Lizenz |
| --- | --- |
| DM Sans | SIL Open Font License 1.1 |
| Inter | SIL Open Font License 1.1 |
| Roboto | SIL Open Font License 1.1 (laut Google Fonts) |
| Atkinson Hyperlegible | SIL Open Font License 1.1 |
| Source Sans 3 | SIL Open Font License 1.1 |

## 5. Programme, die PDH nicht mitliefert

- **ESPHome** (Version laut `scripts/esphome.version`): PDH installiert es bei
  Bedarf in eine eigene Umgebung am Server. Der Python-Teil steht unter MIT, der
  C++-Teil unter GPLv3. Der C++-Teil steckt in jeder ESPPDH-Firmware – siehe
  [LICENSE-EXCEPTION.md](LICENSE-EXCEPTION.md). Beim ersten Bau lädt ESPHome
  PlatformIO und die Werkzeuge der Chip-Hersteller; für sie gelten deren Lizenzen.
- **n8n** steht unter der *Sustainable Use License* und ist damit keine
  Open-Source-Software. PDH liefert n8n nicht mit, sondern leitet nur Anfragen
  an eine getrennt installierte n8n-Instanz weiter.
- **Docker-Testsystem:** Die Abbilder von PostgreSQL, Grafana und code-server
  stammen von ihren Herstellern und stehen unter deren Lizenzen.
