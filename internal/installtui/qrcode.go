package installtui

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	qrcode "github.com/skip2/go-qrcode"
)

func (m *Model) enrollmentQR() string {
	uri, err := url.Parse(m.enrollment.URI)
	if err != nil {
		return "Use the manual secret above."
	}
	// These are the authenticator URI defaults; omit them to reduce QR density.
	query := uri.Query()
	for key, value := range map[string]string{"algorithm": "SHA1", "digits": "6", "period": "30"} {
		if query.Get(key) == value {
			query.Del(key)
		}
	}
	uri.RawQuery = query.Encode()
	code, err := qrcode.New(uri.String(), qrcode.Medium)
	if err != nil {
		return "Use the manual secret above."
	}
	bitmap := code.Bitmap() // Includes the four-module quiet zone for scanning.
	if len(bitmap) > m.contentWidth() || (len(bitmap)+1)/2+8 > m.bodyHeight() {
		return "Enlarge the terminal to scan the QR code, or use the manual secret."
	}
	light, dark := m.currentTheme().qrColors()
	style := lipgloss.NewStyle().Foreground(dark).Background(light)
	var rows []string
	for y := 0; y < len(bitmap); y += 2 {
		var row strings.Builder
		for x := range bitmap[y] {
			top, bottom := bitmap[y][x], y+1 < len(bitmap) && bitmap[y+1][x]
			switch {
			case top && bottom:
				row.WriteRune('█')
			case top:
				row.WriteRune('▀')
			case bottom:
				row.WriteRune('▄')
			default:
				row.WriteByte(' ')
			}
		}
		rows = append(rows, style.Render(row.String()))
	}
	return strings.Join(rows, "\n")
}

func (t theme) qrColors() (light, dark lipgloss.Color) {
	light, dark = t.background, t.background
	brightness := func(color lipgloss.Color) uint64 {
		rgb, _ := strconv.ParseUint(strings.TrimPrefix(string(color), "#"), 16, 24)
		return 299*(rgb>>16) + 587*((rgb>>8)&255) + 114*(rgb&255)
	}
	for _, color := range []lipgloss.Color{t.panel, t.foreground, t.accent, t.selection} {
		if brightness(color) > brightness(light) {
			light = color
		}
		if brightness(color) < brightness(dark) {
			dark = color
		}
	}
	return light, dark
}
