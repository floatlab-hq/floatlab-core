// Command generate-braille-font builds the installer's round-dot fallback font.
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"math/bits"
	"os"
	"slices"
	"unicode/utf16"
)

const (
	unitsPerEm = 1000
	advance    = 600
	ascent     = 1025
	descent    = -275
	glyphCount = 260
)

type point struct {
	x, y    int
	onCurve bool
}

type bounds struct{ left, bottom, right, top int }

func glyph(index int) [][]point {
	switch {
	case index >= 257:
		edges := [][2]int{{375, ascent}, {descent, 375}, {descent, ascent}}[index-257]
		bottom, top := edges[0], edges[1]
		// Fill the entire terminal cell so QR modules have no seams.
		return [][]point{{{0, bottom, true}, {0, top, true}, {advance, top, true}, {advance, bottom, true}}}
	case index <= 1:
		return nil
	}
	positions := [][2]int{{0, 0}, {0, 1}, {0, 2}, {1, 0}, {1, 1}, {1, 2}, {0, 3}, {1, 3}}
	var contours [][]point
	radius := 75 / math.Cos(math.Pi/8)
	for bit, position := range positions {
		if (index-1)&(1<<bit) == 0 {
			continue
		}
		x, y := 150+300*position[0], 875-325*position[1]
		var contour []point
		for i := 7; i >= 0; i-- {
			angle := math.Pi/8 + float64(i)*math.Pi/4
			// All points are off-curve. TrueType inserts the midpoint between each pair.
			contour = append(contour, point{x + int(math.Round(radius*math.Cos(angle))), y + int(math.Round(radius*math.Sin(angle))), false})
		}
		contours = append(contours, contour)
	}
	return contours
}

func u16(to []byte, values ...int) []byte {
	for _, value := range values {
		to = binary.BigEndian.AppendUint16(to, uint16(value))
	}
	return to
}

func u32(to []byte, values ...uint32) []byte {
	for _, value := range values {
		to = binary.BigEndian.AppendUint32(to, value)
	}
	return to
}

// TrueType stores a zero delta in the flag and small deltas as a single byte.
func delta(data []byte, movement int, short, same byte) ([]byte, byte) {
	switch {
	case movement == 0:
		return data, same
	case 0 < movement && movement <= 255:
		return append(data, byte(movement)), short | same
	case -255 <= movement && movement < 0:
		return append(data, byte(-movement)), short
	default:
		return u16(data, movement), 0
	}
}

func outline(contours [][]point) ([]byte, bounds) {
	if len(contours) == 0 {
		return nil, bounds{}
	}
	first := contours[0][0]
	box := bounds{first.x, first.y, first.x, first.y}
	var ends, flags, xs, ys []byte
	var count, x, y int
	for _, contour := range contours {
		for _, p := range contour {
			box = bounds{min(box.left, p.x), min(box.bottom, p.y), max(box.right, p.x), max(box.top, p.y)}
			var flag byte
			if p.onCurve {
				flag = 1
			}
			var xFlag, yFlag byte
			xs, xFlag = delta(xs, p.x-x, 2, 16)
			ys, yFlag = delta(ys, p.y-y, 4, 32)
			flags = append(flags, flag|xFlag|yFlag)
			x, y = p.x, p.y
			count++
		}
		ends = u16(ends, count-1)
	}
	header := u16(nil, len(contours), box.left, box.bottom, box.right, box.top)
	return slices.Concat(header, ends, u16(nil, 0), flags, xs, ys), box
}

func cmap() []byte {
	start := []int{0x2580, 0x2584, 0x2588, 0x2800, 0xffff}
	end := []int{0x2580, 0x2584, 0x2588, 0x28ff, 0xffff}
	glyph := []int{257, 258, 259, 1, 0}
	segments := len(start)
	coverage := u16(nil, 4, 16+8*segments, 0, 2*segments, 8, 2, 2)
	coverage = u16(coverage, end...)
	coverage = u16(coverage, 0)
	coverage = u16(coverage, start...)
	for i, code := range start {
		coverage = u16(coverage, glyph[i]-code)
	}
	coverage = u16(coverage, 0, 0, 0, 0, 0)
	header := u16(nil, 0, 2, 0, 3)
	header = u32(header, 20)
	header = u16(header, 3, 1)
	return slices.Concat(u32(header, 20), coverage)
}

func nameTable() []byte {
	names := []struct {
		id   int
		text string
	}{
		{1, "FloatLab Braille"}, {2, "Regular"}, {3, "FloatLabBraille-Regular-1.0"},
		{4, "FloatLab Braille Regular"}, {5, "Version 1.0"}, {6, "FloatLabBraille-Regular"},
	}
	records := u16(nil, 0, 2*len(names), 6+24*len(names))
	var text []byte
	for _, name := range names {
		records = u16(records, 1, 0, 0, name.id, len(name.text), len(text))
		text = append(text, name.text...)
	}
	for _, name := range names {
		encoded := utf16.Encode([]rune(name.text))
		records = u16(records, 3, 1, 0x409, name.id, 2*len(encoded), len(text))
		for _, c := range encoded {
			text = u16(text, int(c))
		}
	}
	return append(records, text...)
}

func os2() []byte {
	table := u16(nil, 3, advance, 400, 5, 4)
	table = append(table, make([]byte, 32)...) // Subscript, superscript, strikeout, family class and PANOSE.
	table = u32(table, 0, 0x1000, 0x40000, 0)
	table = append(table, "????"...)
	return u16(table, 0x40, 0x2580, 0x28ff, ascent, descent, 0, ascent, -descent,
		0, 0, 0, 0, 0, 0, 0, 32, 0)
}

func post() []byte {
	table := u32(nil, 0x00020000, 0)
	table = u16(table, 0, 0)
	table = u32(table, 1, 0, 0, 0, 0)
	table = u16(table, glyphCount, 0)
	var names []byte
	for index := 1; index < glyphCount; index++ {
		table = u16(table, 257+index)
		name := ""
		if index <= 256 {
			name = fmt.Sprintf("braille%02x", index-1)
		} else {
			name = []string{"upperhalf", "lowerhalf", "fullblock"}[index-257]
		}
		names = append(names, byte(len(name)))
		names = append(names, name...)
	}
	return append(table, names...)
}

func checksum(data []byte) uint32 {
	var sum uint32
	for len(data) >= 4 {
		sum += binary.BigEndian.Uint32(data)
		data = data[4:]
	}
	var remainder [4]byte
	copy(remainder[:], data)
	return sum + binary.BigEndian.Uint32(remainder[:])
}

func font() []byte {
	var glyf, loca, hmtx []byte
	hmtx = u16(hmtx, advance)
	for index := range glyphCount {
		loca = u16(loca, len(glyf)/2)
		outline, box := outline(glyph(index))
		glyf = append(glyf, outline...)
		if len(glyf)%2 != 0 {
			glyf = append(glyf, 0)
		}
		hmtx = u16(hmtx, box.left)
	}
	loca = u16(loca, len(glyf)/2)
	const timestamp = 0xe6f75222
	head := u32(nil, 0x00010000, 0x00010000, 0, 0x5f0f3cf5)
	head = u16(head, 3, unitsPerEm)
	head = u32(head, 0, timestamp, 0, timestamp)
	head = u16(head, 0, descent, advance, ascent, 0, 3, 2, 0, 0)
	hhea := u32(nil, 0x00010000)
	hhea = u16(hhea, ascent, descent, 0, advance, 0, 0, advance, 1, 0, 0, 0, 0, 0, 0, 0, 1)
	maxp := u32(nil, 0x00010000)
	maxp = u16(maxp, glyphCount, 64, 8, 0, 0, 2, 0, 0, 0, 0, 0, 0, 0, 0)
	tables := map[string][]byte{
		"OS/2": os2(), "cmap": cmap(), "glyf": glyf, "head": head, "hhea": hhea,
		"hmtx": hmtx, "loca": loca, "maxp": maxp, "name": nameTable(), "post": post(),
	}
	n := len(tables)
	selector := bits.Len(uint(n)) - 1
	search := 16 << selector
	header := u16(u32(nil, 0x00010000), n, search, selector, n*16-search)
	start := len(header) + n*16
	offsets := make(map[string]int, n)
	var body []byte
	for _, tag := range []string{"head", "hhea", "maxp", "OS/2", "hmtx", "cmap", "loca", "glyf", "name", "post"} {
		offsets[tag] = start + len(body)
		body = append(body, tables[tag]...)
		for len(body)%4 != 0 {
			body = append(body, 0)
		}
	}
	for _, tag := range []string{"OS/2", "cmap", "glyf", "head", "hhea", "hmtx", "loca", "maxp", "name", "post"} {
		data := tables[tag]
		header = append(header, tag...)
		header = u32(header, checksum(data), uint32(offsets[tag]), uint32(len(data)))
	}
	adjustment := offsets["head"] + 8
	result := append(header, body...)
	binary.BigEndian.PutUint32(result[adjustment:], 0xb1b0afba-checksum(result))
	return result
}

func main() {
	output := flag.String("output", "floatlab-appliance-image/fonts/FloatLabBraille-Regular.ttf", "output font path")
	flag.Parse()
	if flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}
	if err := os.WriteFile(*output, font(), 0644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
