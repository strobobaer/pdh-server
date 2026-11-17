# Labor-Gegenstelle für SNMP v3 (nur zum Testen, nie produktiv): net-snmp snmpd.
FROM alpine:3.22
RUN apk add --no-cache net-snmp
COPY snmpd.conf /etc/snmp/snmpd.conf
EXPOSE 161/udp
# -f Vordergrund, -Lo Protokoll auf die Konsole, -C nur die genannte Konfiguration lesen
CMD ["snmpd", "-f", "-Lo", "-C", "-c", "/etc/snmp/snmpd.conf", "udp:161"]
