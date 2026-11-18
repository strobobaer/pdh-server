package esppdh

import (
	"slices"
	"strings"
)

// Platinen für ESPPDH. Je Platine stehen hier die Anschlüsse, die die Bausteine nutzen dürfen.
// Grundlage sind die Pin-Prüfungen von ESPHome (components/esp32/gpio_*.py,
// components/esp8266/gpio.py, components/adc) in der festgelegten Version und die
// Anschlussbelegung der Platinen. Ausgelassen sind Pins für Flash/PSRAM, USB, den
// Protokoll-Anschluss (UART0), Ethernet, Leuchtdioden und Taster der Platine sowie
// Strapping-Pins, die den Start beeinflussen (GPIO5 der ESP32 nur SDIO-Zeiten – frei).
// Analog misst nur ADC1: ADC2 ist bei eingeschaltetem WLAN belegt.

// BoardOther: „Andere Platine“ mit Platinen-Kennung von ESPHome als Freitext. Für sie gibt es
// keine Pin-Listen und keine Pin-Prüfung.
const BoardOther = "other"

// Board ist eine Platine zur Auswahl.
//   - Platform: "esp32" oder "esp8266"; bei BoardOther leer (folgt aus der Platinen-Kennung).
//   - ESPHomeBoard: Platinen-Kennung für ESPHome (board:); bei BoardOther leer.
//   - ADCPins: Pins, die analog messen können (SCT-013).
//   - InputPins: Pins für digitale Bausteine (S0, Stückzähler, PZEM RX/TX): mit internem
//     Pull-up, auch als Ausgang nutzbar, von der Platine nicht belegt.
type Board struct {
	Key, Label, Platform, ESPHomeBoard string
	Ethernet                           bool
	ADCPins, InputPins                 []string

	eth      *ethernetPHY // Ethernet-Anschluss, nur bei Ethernet
	adcScale float64      // Spannungsteiler vor dem Analog-Pin (D1 mini: 3,2); 0 = keiner
	maxUARTs int          // freie serielle Anschlüsse für PZEM-004T; 0 = keine Grenze
}

// ethernetPHY: Ethernet-Baustein einer Platine (RMII, Abschnitt ethernet: von ESPHome).
type ethernetPHY struct {
	typ, mdc, mdio, clkMode, clkPin, powerPin string
	phyAddr                                   int
}

// PinsFor: Pins zur Auswahl für ein Pin-Feld (analog oder digital); nil für andere Felder.
func (b Board) PinsFor(f Field) []string {
	if f.Type != "pin" {
		return nil
	}
	if f.Analog {
		return slices.Clone(b.ADCPins)
	}
	return slices.Clone(b.InputPins)
}

var boards = []Board{
	{
		// Espressif ESP32-DevKitC (ESP32-WROOM-32). UART0 (GPIO1/3) für das Protokoll und
		// UART1/UART2 frei; GPIO34–39 nur Eingang ohne Pull-up.
		Key: "esp32dev", Label: "ESP32 DevKit", Platform: "esp32", ESPHomeBoard: "esp32dev",
		ADCPins: []string{"GPIO32", "GPIO33", "GPIO34", "GPIO35", "GPIO36", "GPIO39"},
		InputPins: []string{"GPIO4", "GPIO5", "GPIO13", "GPIO14", "GPIO16", "GPIO17", "GPIO18", "GPIO19",
			"GPIO21", "GPIO22", "GPIO23", "GPIO25", "GPIO26", "GPIO27", "GPIO32", "GPIO33"},
		maxUARTs: 2,
	},
	{
		// Espressif ESP32-S3-DevKitC-1. Ausgelassen: GPIO0/3/45/46 (Strapping), 19/20 (USB),
		// 26–37 (Flash/PSRAM), 38/48 (Leuchtdiode je nach Ausgabe), 43/44 (UART0), 47 (bei
		// 1,8-V-Flash). Das Protokoll läuft über USB, alle drei UARTs sind frei.
		Key: "esp32-s3", Label: "ESP32-S3 DevKitC", Platform: "esp32", ESPHomeBoard: "esp32-s3-devkitc-1",
		ADCPins: []string{"GPIO1", "GPIO2", "GPIO4", "GPIO5", "GPIO6", "GPIO7", "GPIO8", "GPIO9", "GPIO10"},
		InputPins: []string{"GPIO1", "GPIO2", "GPIO4", "GPIO5", "GPIO6", "GPIO7", "GPIO8", "GPIO9", "GPIO10",
			"GPIO11", "GPIO12", "GPIO13", "GPIO14", "GPIO15", "GPIO16", "GPIO17", "GPIO18", "GPIO21",
			"GPIO39", "GPIO40", "GPIO41", "GPIO42"},
		maxUARTs: 3,
	},
	{
		// Espressif ESP32-C3-DevKitM-1. Ausgelassen: GPIO2/8/9 (Strapping, GPIO8 Leuchtdiode),
		// 11–17 (Flash), 18/19 (USB), 20/21 (UART0). Das Protokoll läuft über USB.
		Key: "esp32-c3", Label: "ESP32-C3 DevKitM", Platform: "esp32", ESPHomeBoard: "esp32-c3-devkitm-1",
		ADCPins:   []string{"GPIO0", "GPIO1", "GPIO3", "GPIO4"},
		InputPins: []string{"GPIO0", "GPIO1", "GPIO3", "GPIO4", "GPIO5", "GPIO6", "GPIO7", "GPIO10"},
		maxUARTs:  2,
	},
	{
		// WeMos D1 mini (ESP8266), Pins mit der Beschriftung der Platine. Ausgelassen: D0
		// (GPIO16: kein Pull-up, kein Interrupt), D3/D4/D8 (Strapping, D4 Leuchtdiode), TX/RX.
		// A0 hat einen Spannungsteiler 220k/100k (0–3,2 V an A0 = 0–1 V am Chip).
		Key: "d1-mini", Label: "ESP8266 D1 mini", Platform: "esp8266", ESPHomeBoard: "d1_mini",
		ADCPins:   []string{"A0"},
		InputPins: []string{"D1", "D2", "D5", "D6", "D7"},
		adcScale:  3.2,
	},
	{
		// WT32-ETH01: Ethernet LAN8720 (MDC 23, MDIO 18, Takt von außen an GPIO0, Versorgung
		// GPIO16, RMII 19/21/22/25/26/27). Frei auf der Stiftleiste: IO4, IO5 (RXD), IO17 (TXD),
		// IO14, IO32 (CFG), IO33 (485_EN); IO35/36/39 nur Eingang.
		Key: "wt32-eth01", Label: "WT32-ETH01 (Ethernet)", Platform: "esp32", ESPHomeBoard: "wt32-eth01", Ethernet: true,
		ADCPins:   []string{"GPIO32", "GPIO33", "GPIO35", "GPIO36", "GPIO39"},
		InputPins: []string{"GPIO4", "GPIO5", "GPIO14", "GPIO17", "GPIO32", "GPIO33"},
		eth:       &ethernetPHY{typ: "LAN8720", mdc: "GPIO23", mdio: "GPIO18", clkMode: "CLK_EXT_IN", clkPin: "GPIO0", phyAddr: 1, powerPin: "GPIO16"},
		maxUARTs:  2,
	},
	{
		// Olimex ESP32-POE: Ethernet LAN8720 (MDC 23, MDIO 18, Takt aus GPIO17, Versorgung
		// GPIO12). Ausgelassen: GPIO34 (Taster), GPIO39 (Erkennung der Versorgung), 2/14/15
		// (SD-Karte).
		Key: "olimex-poe", Label: "Olimex ESP32-POE (Ethernet)", Platform: "esp32", ESPHomeBoard: "esp32-poe", Ethernet: true,
		ADCPins:   []string{"GPIO32", "GPIO33", "GPIO35", "GPIO36"},
		InputPins: []string{"GPIO4", "GPIO5", "GPIO13", "GPIO16", "GPIO32", "GPIO33"},
		eth:       &ethernetPHY{typ: "LAN8720", mdc: "GPIO23", mdio: "GPIO18", clkMode: "CLK_OUT", clkPin: "GPIO17", phyAddr: 0, powerPin: "GPIO12"},
		maxUARTs:  2,
	},
	{Key: BoardOther, Label: "Andere Platine"},
}

// Boards: alle Platinen zur Auswahl, in fester Reihenfolge (Kopien).
func Boards() []Board {
	out := make([]Board, len(boards))
	for i, b := range boards {
		out[i] = b.clone()
	}
	return out
}

// BoardByKey: Platine zur Kennung.
func BoardByKey(key string) (Board, bool) {
	for _, b := range boards {
		if b.Key == key {
			return b.clone(), true
		}
	}
	return Board{}, false
}

func (b Board) clone() Board {
	b.ADCPins = slices.Clone(b.ADCPins)
	b.InputPins = slices.Clone(b.InputPins)
	return b
}

// esp8266Boards: Platinen-Kennungen von ESPHome 2026.10.2 für ESP8266
// (components/esp8266/boards.py). Jede andere Kennung gilt als ESP32-Familie;
// eine unbekannte meldet ESPHome beim Bauen.
var esp8266Boards = map[string]bool{
	"agruminolemon": true, "d1_mini_lite": true, "d1_mini": true, "d1_mini_pro": true, "d1_wroom_02": true,
	"d1": true, "eduinowifi": true, "esp01_1m": true, "esp01": true, "esp07": true, "esp07s": true,
	"esp12e": true, "esp210": true, "esp8285": true, "espduino": true, "espectro": true, "espino": true,
	"espinotee": true, "espmxdevkit": true, "espresso_lite_v1": true, "espresso_lite_v2": true,
	"esp_wroom_02": true, "gen4iod": true, "heltec_wifi_kit_8": true, "huzzah": true, "inventone": true,
	"modwifi": true, "nodemcu": true, "nodemcuv2": true, "oak": true, "phoenix_v1": true, "phoenix_v2": true,
	"sonoff_basic": true, "sonoff_s20": true, "sonoff_sv": true, "sonoff_th": true, "thingdev": true,
	"thing": true, "wifiduino": true, "wifinfo": true, "wifi_slot": true, "wio_link": true, "wio_node": true,
	"xinabox_cw01": true,
}

// customPlatform: Plattform zur Platinen-Kennung einer „Anderen Platine“. Groß-/Kleinschreibung
// und „-“ statt „_“ gleicht ESPHome bei ESP8266 selbst aus (esp8266/__init__.py).
func customPlatform(id string) string {
	if esp8266Boards[strings.ReplaceAll(strings.ToLower(id), "-", "_")] {
		return "esp8266"
	}
	return "esp32"
}
