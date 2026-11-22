package web

// Letzte Werte der ESPPDH-Geräte für die Anzeige im PDH (Geräteseite, Reiter „ESPPDH“ an der
// Anlage, Anlagenterminal, Schnellübersicht). Quelle ist der Speicher des eingebauten Brokers
// (mqttbroker.TopicValues); welches Thema was ist, sagt der Erzeuger (esppdh.Topics). Keine
// Durchreiche der Geräte-Webseite: nur ein Direktlink, wenn die gemeldete Adresse bestätigt ist.

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"pdh/internal/core/mqttbroker"
	"pdh/internal/modules/esppdh"
)

// topicSource: was die Anzeige vom Broker braucht (Tests ersetzen ihn über Handler.topicSrc).
type topicSource interface {
	TopicValues(id, prefix string) ([]mqttbroker.TopicValue, bool)
	ClientRemoteIP(id, username string) (string, bool)
}

func (h *Handler) topics() topicSource {
	if h.topicSrc != nil {
		return h.topicSrc
	}
	if h.mqttBrokers != nil {
		return h.mqttBrokers
	}
	return nil
}

type esppdhValueRow struct {
	Name, Value, Unit, Age string
	Stale                  bool // älter als 3 × Aktualisierungsintervall oder noch nie gemeldet
}

type esppdhGroupView struct {
	Name string
	Rows []esppdhValueRow
}

// esppdhValuesView: ein Gerät mit seinen letzten Werten.
type esppdhValuesView struct {
	ID, Name, Slug, Path            string
	Online, Available               bool // Available: Werte lesbar (Broker läuft an diesem Server)
	Live                            esppdhLive
	Handshake                       handshakeView
	Address, DirectURL, AddressNote string
	Groups                          []esppdhGroupView
	infra                           string // Anlage des Geräts (Verteilung auf Karten)
}

// esppdhAddressOK: Darf die vom Gerät gemeldete Adresse verlinkt werden? Nur eine IPv4-Adresse,
// die gleich der Absenderadresse seiner MQTT-Verbindung ist, keine Sonderadresse und keine
// Adresse dieses Servers, und – wenn sein Netz bekannt ist – darin liegt.
func esppdhAddressOK(reported, remoteIP, subnet string, own []net.IP) (bool, string) {
	if reported == "" {
		return false, "Adresse noch nicht gemeldet"
	}
	ip := net.ParseIP(reported).To4()
	if ip == nil {
		return false, "keine IPv4-Adresse"
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() || ip.Equal(net.IPv4bcast) {
		return false, "Adresse unplausibel"
	}
	for _, o := range own {
		if o.Equal(ip) {
			return false, "Adresse unplausibel"
		}
	}
	if r := net.ParseIP(remoteIP); r == nil || !r.Equal(ip) {
		return false, "Adresse nicht bestätigt"
	}
	if subnet != "" {
		if _, n, err := net.ParseCIDR(subnet); err == nil && !n.Contains(ip) {
			return false, "Adresse außerhalb des Netzes " + subnet
		}
	}
	return true, ""
}

// ownAddrs: Adressen dieses Servers (Schnittstellen).
func ownAddrs() []net.IP {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []net.IP
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			out = append(out, n.IP)
		}
	}
	return out
}

// esppdhValues: Werte-Ansicht je Gerät (Geräte mit Bausteinen). Je Broker ein Abruf.
func (h *Handler) esppdhValues(ctx context.Context, devs []esppdhDevice, now time.Time) []esppdhValuesView {
	src := h.topics()
	type brokerVals struct {
		vals map[string]mqttbroker.TopicValue
		ok   bool
	}
	cache := map[string]brokerVals{}
	live := map[string]esppdhLive{}
	if h.db != nil {
		live = h.esppdhLiveStates(ctx, devs)
	}
	own := ownAddrs()
	out := make([]esppdhValuesView, 0, len(devs))
	for _, d := range devs {
		v := esppdhValuesView{ID: d.ID, Name: d.Name, Slug: d.Slug, Path: d.InfraPath, Live: live[d.ID], infra: d.InfraID}
		v.Online = v.Live.Key == "online"
		bv, seen := cache[d.ConnID]
		if !seen {
			if src != nil {
				list, ok := src.TopicValues(d.ConnID, mqttbroker.CachePrefix)
				bv.ok = ok
				bv.vals = make(map[string]mqttbroker.TopicValue, len(list))
				for _, x := range list {
					bv.vals[x.Topic] = x
				}
			}
			cache[d.ConnID] = bv
		}
		v.Available = bv.ok
		topics, err := esppdh.Topics(esppdh.Device{Slug: d.Slug, BoardKey: d.BoardKey, CustomBoard: d.CustomBoard, Wired: d.Wired, Blocks: esppdhBlocksOf(d.Blocks)})
		if err != nil {
			v.AddressNote = "Bausteine ungültig – Werte nicht zuzuordnen"
		}
		groupIdx := map[string]int{}
		for _, ti := range topics {
			x, has := bv.vals[ti.Topic]
			switch {
			case ti.Handshake:
				at := time.Time{}
				if has && bv.ok {
					at = x.At // Broker hier aus (Spiegel): unbekannt statt „steht“
				}
				v.Handshake = handshakeFromValue("esp-"+d.ID, ti.Name, x.Payload, at, ti.Interval, now)
				continue
			case strings.HasSuffix(ti.Topic, "/info/adresse"):
				if has {
					v.Address = strings.TrimSpace(x.Payload)
				}
				continue
			case strings.HasSuffix(ti.Topic, "/info/firmware"):
				continue // Kennung des Baus, kein Messwert (Updates übers Netz)
			}
			if !bv.ok {
				continue
			}
			row := esppdhValueRow{Name: ti.Name, Unit: ti.Unit, Value: "–", Stale: true}
			if has {
				row.Value, row.Age = strings.TrimSpace(x.Payload), terminalAge(now, x.At)
				row.Stale = ti.Interval > 0 && now.Sub(x.At) > 3*ti.Interval
			}
			i, ok := groupIdx[ti.Group]
			if !ok {
				i = len(v.Groups)
				groupIdx[ti.Group] = i
				v.Groups = append(v.Groups, esppdhGroupView{Name: ti.Group})
			}
			v.Groups[i].Rows = append(v.Groups[i].Rows, row)
		}
		if v.Address != "" && src != nil {
			remote, _ := src.ClientRemoteIP(d.ConnID, esppdhUserOf(d.Slug))
			ok, note := esppdhAddressOK(v.Address, remote, d.NetSubnet, own)
			if ok && d.WebEnabled {
				v.DirectURL = "http://" + v.Address + "/"
			}
			if !ok {
				v.AddressNote = note
			}
		}
		out = append(out, v)
	}
	return out
}

// EsppdhValuesWeb: GET /import/esppdh/{id}/values – Werte-Bereich der Geräteseite (htmx, alle 10 s).
func (h *Handler) EsppdhValuesWeb(w http.ResponseWriter, r *http.Request) {
	id, ok := h.esppdhRecordAction(w, r, permESPView)
	if !ok {
		return
	}
	d, err := h.esppdhLoad(r.Context(), id)
	if err != nil {
		h.esppdhLoadFailed(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	v := h.esppdhValues(r.Context(), []esppdhDevice{d}, time.Now())[0]
	h.renderFragment(w, "esppdh-values", map[string]any{"V": v, "Link": false})
}
