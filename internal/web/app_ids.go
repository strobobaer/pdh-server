package web

// PDH-App (Paket 1): feste Kennungen und Rechte. Die Werte stehen auch in
// Migration 187_app_grundlage.up.sql.
const (
	// appSystemUserID: inaktiver Systembenutzer „PDH-App“ (Urheber von Vorgängen aus der App).
	appSystemUserID = "00000000-0000-4000-8000-0000000a0001"
	// appAdminGroupID: Gruppe „App-Administratoren“ (bekommt Anfragen für Geräte).
	appAdminGroupID = "00000000-0000-4000-8000-0000000a0002"

	permAppUse     = "app.use"
	permAppCreate  = "app.create"
	permAppCockpit = "app.cockpit"
	permAppSensors = "app.sensors.view"
	permAppAdmin   = "app.admin"

	// appRef: Bereich „App“ für areaAdminPerm.
	appRef = "app"
)

// Öffentliche Schnittstelle der App: Stufe und Einstellungsschlüssel (app_settings).
const (
	appAPILevel = 1

	keyAppAddresses     = "app.addresses"       // zusätzliche Server-Adressen, eine je Zeile
	keyAppAllowPrevious = "app.allow_previous"  // "1"/"0", Grundwert "1"
	keyAppLatestPrefix  = "app.latest_version." // + ios | android
	keyAppPrevPrefix    = "app.previous_version."
	keyAppStorePrefix   = "app.store_url."
)
