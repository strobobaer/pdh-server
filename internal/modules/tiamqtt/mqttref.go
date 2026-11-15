package tiamqtt

// Go-Gegenstück zum MQTT-3.1.1-Kodierer im SCL-Baustein der eigenständigen
// Variante: dieselben Pakete Byte für Byte. Die Tests lesen sie mit dem
// Kodierer des PDH-Brokers zurück und schicken sie an den echten Broker.

// RemainingLength kodiert die Restlänge eines MQTT-Pakets (7 Bit je Byte,
// oberstes Bit = es folgt ein weiteres Byte).
func RemainingLength(n int) []byte {
	var out []byte
	for {
		b := byte(n % 128)
		n /= 128
		if n > 0 {
			b |= 0x80
		}
		out = append(out, b)
		if n == 0 {
			return out
		}
	}
}

func appendMQTTString(b []byte, s string) []byte {
	b = append(b, byte(len(s)>>8), byte(len(s)))
	return append(b, s...)
}

// ConnectPacket baut CONNECT mit Clean Session. Benutzer- und Kennwort-
// Flag nur, wenn gesetzt; ein Kennwort nur zusammen mit einem Benutzer.
func ConnectPacket(clientID, user, pass string, keepAlive uint16) []byte {
	flags := byte(0x02)
	body := []byte{0x00, 0x04, 'M', 'Q', 'T', 'T', 0x04, 0, byte(keepAlive >> 8), byte(keepAlive)}
	body = appendMQTTString(body, clientID)
	if user != "" {
		flags |= 0x80
		body = appendMQTTString(body, user)
		if pass != "" {
			flags |= 0x40
			body = appendMQTTString(body, pass)
		}
	}
	body[7] = flags
	return append(append([]byte{0x10}, RemainingLength(len(body))...), body...)
}

// PublishPacket baut PUBLISH mit QoS 0.
func PublishPacket(topic string, payload []byte, retain bool) []byte {
	hdr := byte(0x30)
	if retain {
		hdr |= 0x01
	}
	body := appendMQTTString(nil, topic)
	body = append(body, payload...)
	return append(append([]byte{hdr}, RemainingLength(len(body))...), body...)
}

// PingReq ist PINGREQ.
func PingReq() []byte { return []byte{0xC0, 0x00} }

// Disconnect ist DISCONNECT.
func Disconnect() []byte { return []byte{0xE0, 0x00} }
