#!/usr/bin/env python3
"""Build the installer's round-dot fallback font (requires fonttools)."""
import math
from pathlib import Path
from fontTools.fontBuilder import FontBuilder
from fontTools.pens.ttGlyphPen import TTGlyphPen

builder = FontBuilder(1000, isTTF=True)
names = ['.notdef'] + [f'braille{value:02x}' for value in range(256)]
blocks = {0x2580: ('upperhalf', 375, 1025), 0x2584: ('lowerhalf', -275, 375), 0x2588: ('fullblock', -275, 1025)}
names += [block[0] for block in blocks.values()]
builder.setupGlyphOrder(names)
cmap = {0x2800 + value: names[value + 1] for value in range(256)}
cmap.update({code: block[0] for code, block in blocks.items()})
builder.setupCharacterMap(cmap)
glyphs = {}
positions = [(0, 0), (0, 1), (0, 2), (1, 0), (1, 1), (1, 2), (0, 3), (1, 3)]
for index, name in enumerate(names):
    pen = TTGlyphPen(None)
    if 0 < index <= 256:
        for bit, (column, row) in enumerate(positions):
            if (index - 1) & (1 << bit):
                x, y = 150 + column * 300, 875 - row * 325
                radius = 75 / math.cos(math.pi / 8)
                points = [(round(x + radius * math.cos(math.pi / 8 + n * math.pi / 4)),
                           round(y + radius * math.sin(math.pi / 8 + n * math.pi / 4)))
                          for n in range(8)]
                pen.qCurveTo(*reversed(points), None)
                pen.closePath()
    glyphs[name] = pen.glyph()
# Fill the entire terminal cell so QR modules have no font-shaped seams.
for name, bottom, top in blocks.values():
    pen = TTGlyphPen(None)
    pen.moveTo((0, bottom))
    pen.lineTo((0, top))
    pen.lineTo((600, top))
    pen.lineTo((600, bottom))
    pen.closePath()
    glyphs[name] = pen.glyph()
builder.setupGlyf(glyphs)
for glyph in glyphs.values():
    glyph.recalcBounds(glyphs)
builder.setupHorizontalMetrics({name: (600, getattr(glyphs[name], 'xMin', 0)) for name in names})
builder.setupHorizontalHeader(ascent=1025, descent=-275)
builder.setupNameTable({'familyName': 'FloatLab Braille', 'styleName': 'Regular',
                       'uniqueFontIdentifier': 'FloatLabBraille-Regular-1.0',
                       'fullName': 'FloatLab Braille Regular', 'psName': 'FloatLabBraille-Regular',
                       'version': 'Version 1.0'})
builder.setupOS2(sTypoAscender=1025, sTypoDescender=-275,
                usWinAscent=1025, usWinDescent=275, fsSelection=0x40)
builder.setupPost(isFixedPitch=1)
builder.setupMaxp()
builder.save(Path(__file__).resolve().parents[1] / 'floatlab-appliance-image/fonts/FloatLabBraille-Regular.ttf')
