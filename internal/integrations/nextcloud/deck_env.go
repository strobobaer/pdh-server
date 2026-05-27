package nextcloud

import (
	"os"
	"strings"
)

func DeckClientFromEnv() *DeckClient {
	return NewDeckClient(DeckConfig{
		Enabled:            truthy(os.Getenv("PDH_NEXTCLOUD_DECK_ENABLED")),
		BaseURL:            envDefault("PDH_NEXTCLOUD_DECK_BASEURL", envDefault("PDH_NEXTCLOUD_BASEURL", "https://cloud.strobl-home.net")),
		Username:           envDefault("PDH_NEXTCLOUD_DECK_USERNAME", os.Getenv("PDH_NEXTCLOUD_USERNAME")),
		Password:           envDefault("PDH_NEXTCLOUD_DECK_PASSWORD", os.Getenv("PDH_NEXTCLOUD_PASSWORD")),
		BoardID:            os.Getenv("PDH_NEXTCLOUD_DECK_BOARD_ID"),
		TicketStackID:      os.Getenv("PDH_NEXTCLOUD_DECK_STACK_TICKETS_ID"),
		FaultStackID:       os.Getenv("PDH_NEXTCLOUD_DECK_STACK_FAULTS_ID"),
		MaintenanceStackID: os.Getenv("PDH_NEXTCLOUD_DECK_STACK_MAINTENANCE_ID"),
		PublicPDHURL:       envDefault("PDH_PUBLIC_URL", "https://pdh.strobl-home.net"),
	})
}

func truthy(v string) bool {
	v = strings.TrimSpace(strings.ToLower(v))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func envDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
