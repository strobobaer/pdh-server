# Helfer des Zeugen (updater.sh): zieht den Zeugen-Container auf den Stand der
# beiden Server nach. Build-Kontext ist die Repo-Wurzel (siehe compose.yaml).
FROM docker:27-cli
LABEL org.opencontainers.image.licenses="AGPL-3.0-or-later"
RUN apk add --no-cache git bash jq
COPY deploy/ha-witness/updater.sh /usr/local/bin/pdh-ha-updater
COPY LICENSE COPYING.md LICENSE-EXCEPTION.md THIRD_PARTY_LICENSES.md /usr/share/licenses/pdh/
COPY third_party/notices /usr/share/licenses/pdh/notices
# Windows-Checkout (CRLF) darf den Start nicht verhindern
RUN sed -i 's/\r$//' /usr/local/bin/pdh-ha-updater \
    && chmod 0755 /usr/local/bin/pdh-ha-updater
ENTRYPOINT ["/usr/local/bin/pdh-ha-updater"]
