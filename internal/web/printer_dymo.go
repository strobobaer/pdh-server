package web

import (
	"fmt"
	"strings"
	"time"
)

// Dymo LabelWriter (450, 550, 5XL, Wireless): gedruckt wird ueber
// DYMO Connect auf dem PC, an dem der Drucker haengt. Dessen lokaler
// Webdienst (https://127.0.0.1:41951/DYMO/DLS/Printing/…) nimmt das
// Etikett als XML entgegen – der Browser schickt es dorthin, der PDH-Server
// erzeugt nur das XML. Koordinaten in Twips (1/1440 Zoll).

type dymoLabelType struct {
	Key, Label, PaperName string
	ShortMM, LongMM       float64
}

// dymoLabelTypes: gaengige Etiketten (Masse quer zur / entlang der Rolle).
var dymoLabelTypes = []dymoLabelType{
	{"11354", "11354 Mehrzweck 57 × 32 mm", "11354 Multi-Purpose", 32, 57},
	{"11352", "11352 Rücksende 54 × 25 mm", "11352 Return Address Int", 25, 54},
	{"99012", "99012 Adresse groß 89 × 36 mm", "99012 Large Address", 36, 89},
	{"99010", "99010 Adresse 89 × 28 mm", "99010 Standard Address", 28, 89},
	{"11355", "11355 Mehrzweck 51 × 19 mm", "11355 Multi-Purpose", 19, 51},
}

func dymoLabelTypeByKey(k string) dymoLabelType {
	for _, t := range dymoLabelTypes {
		if t.Key == k {
			return t
		}
	}
	return dymoLabelTypes[0]
}

func twips(mm float64) int { return int(mm / 25.4 * 1440) }

func xmlEsc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&apos;").Replace(s)
}

const dymoColors = `<ForeColor Alpha="255" Red="0" Green="0" Blue="0" /><BackColor Alpha="0" Red="255" Green="255" Blue="255" />`

func dymoText(name, text string, size float64, bold bool, x, y, w, h int, align string) string {
	b := "False"
	if bold {
		b = "True"
	}
	return fmt.Sprintf(`<ObjectInfo><TextObject><Name>%s</Name>%s<LinkedObjectName></LinkedObjectName><Rotation>Rotation0</Rotation><IsMirrored>False</IsMirrored><IsVariable>False</IsVariable>`+
		`<HorizontalAlignment>%s</HorizontalAlignment><VerticalAlignment>Top</VerticalAlignment><TextFitMode>ShrinkToFit</TextFitMode><UseFullFontHeight>True</UseFullFontHeight><Verticalized>False</Verticalized>`+
		`<StyledText><Element><String>%s</String><Attributes><Font Family="Arial" Size="%.1f" Bold="%s" Italic="False" Underline="False" Strikeout="False" /><ForeColor Alpha="255" Red="0" Green="0" Blue="0" /></Attributes></Element></StyledText>`+
		`</TextObject><Bounds X="%d" Y="%d" Width="%d" Height="%d" /></ObjectInfo>`,
		name, dymoColors, align, xmlEsc(text), size, b, x, y, w, h)
}

func dymoQR(text string, x, y, size int) string {
	return fmt.Sprintf(`<ObjectInfo><BarcodeObject><Name>QR</Name>%s<LinkedObjectName></LinkedObjectName><Rotation>Rotation0</Rotation><IsMirrored>False</IsMirrored><IsVariable>False</IsVariable>`+
		`<Text>%s</Text><Type>QRCode</Type><Size>Small</Size><TextPosition>None</TextPosition>`+
		`<TextFont Family="Arial" Size="8" Bold="False" Italic="False" Underline="False" Strikeout="False" /><CheckSumFont Family="Arial" Size="8" Bold="False" Italic="False" Underline="False" Strikeout="False" />`+
		`<TextEmbedding>None</TextEmbedding><ECLevel>0</ECLevel><HorizontalAlignment>Center</HorizontalAlignment><QuietZonesPadding Left="0" Top="0" Right="0" Bottom="0" />`+
		`</BarcodeObject><Bounds X="%d" Y="%d" Width="%d" Height="%d" /></ObjectInfo>`, dymoColors, xmlEsc(text), x, y, size, size)
}

func dymoLabelOpen(t dymoLabelType, paperName string) string {
	short, long := twips(t.ShortMM), twips(t.LongMM)
	return fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?><DieCutLabel Version="8.0" Units="twips"><PaperOrientation>Landscape</PaperOrientation><Id>PDH</Id>`+
		`<PaperName>%s</PaperName><DrawCommands><RoundRectangle X="0" Y="0" Width="%d" Height="%d" Rx="180" Ry="180" /></DrawCommands>`,
		xmlEsc(firstNonEmpty(paperName, t.PaperName)), short, long)
}

// dymoPartLabel: Lagerplatz-Etikett (quer: QR links, Texte rechts).
func dymoPartLabel(it labelItem, t dymoLabelType, paperName string, showCat bool) string {
	W, H := twips(t.LongMM), twips(t.ShortMM) // Querformat
	m := twips(1.5)
	q := H - 2*m
	var b strings.Builder
	b.WriteString(dymoLabelOpen(t, paperName))
	x := m
	if it.URL != "" {
		b.WriteString(dymoQR(it.URL, m, m, q))
		x = m + q + m
	}
	tw := W - x - m
	small := t.ShortMM < 30
	loc := it.headText()
	lh := H / 4
	b.WriteString(dymoText("LAGERPLATZ", loc, map[bool]float64{true: 10, false: 12}[small], true, x, m, tw, lh, "Left"))
	nameH := H / 3
	b.WriteString(dymoText("NAME", it.Name, map[bool]float64{true: 7, false: 9}[small], true, x, m+lh, tw, nameH, "Left"))
	if showCat && it.Category != "" && !small {
		b.WriteString(dymoText("KATEGORIE", it.Category, 6, false, x, m+lh+nameH, tw, H/8, "Left"))
	}
	by := H - m - H/5
	b.WriteString(dymoText("TEILENR", it.numText(), map[bool]float64{true: 7, false: 9}[small], true, x, by, tw*6/10, H/5, "Left"))
	b.WriteString(dymoText("MIN", it.rightText(), map[bool]float64{true: 7, false: 9}[small], true, x+tw*6/10, by, tw*4/10, H/5, "Right"))
	b.WriteString(`</DieCutLabel>`)
	return b.String()
}

// dymoTestLabel: Testetikett mit Druckername, Datum und QR-Code.
func dymoTestLabel(printerName, url string, t dymoLabelType, paperName string) string {
	W, H := twips(t.LongMM), twips(t.ShortMM)
	m := twips(1.5)
	q := H - 2*m
	var b strings.Builder
	b.WriteString(dymoLabelOpen(t, paperName))
	x := m
	if url != "" {
		b.WriteString(dymoQR(url, m, m, q))
		x = m + q + m
	}
	tw := W - x - m
	b.WriteString(dymoText("TITEL", "PDH Testetikett", 12, true, x, m, tw, H/3, "Left"))
	b.WriteString(dymoText("DRUCKER", printerName, 8, false, x, m+H/3, tw, H/4, "Left"))
	b.WriteString(dymoText("DATUM", time.Now().Format("02.01.2006 15:04")+" · ÄÖÜ äöü ß", 8, false, x, m+H/3+H/4, tw, H/4, "Left"))
	b.WriteString(`</DieCutLabel>`)
	return b.String()
}
