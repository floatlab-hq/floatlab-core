# Dashboard design-system mapping

Reference: [Figma dashboard, frame 1700:20300](https://www.figma.com/design/yJzrnkHhurVIrwIpq5UINP/FloatLab?node-id=1700-20300).

## Colors

Use roles, not Everforest hex literals, in components. Every role follows the global theme picker.

| Figma use | Source color | UI token | Omarchy key |
| --- | --- | --- | --- |
| Page canvas | #2D353B | --surface-canvas | background |
| Rows, search, controls | #343F44 | --surface-default | lighter_background |
| Primary text and headings | #E6E2CC (near palette) | --text-primary | bright_foreground |
| Secondary text | #D3C6AA | --text-secondary | foreground |
| Node plate block | #D3C6AA | --omarchy-foreground | foreground |
| Text/shape on plate | #343F44 / #142326 (near palette) | --text-inverse | lighter_background |
| Running, standby, TLS | #A7C080 | --status-success | green |
| Error events | #E67E80 | --status-error | red |
| Warning events | #FFCB7C (near palette) | --status-warning | yellow |
| Brand and external links | #7FBBB3 | --omarchy-accent | accent |
| Command hint | #D3C6AA at 60% | --text-placeholder | foreground at 60% |

Everforest defines foreground and bright_foreground as the same value. Hierarchy is
therefore carried by font size and weight rather than inventing a new palette color.
Near-palette values are intentionally normalized. Solid colors inside imported
monochrome brand/app SVGs are rendered through CSS masks so the original silhouettes
inherit the relevant theme role. App logos remain data-driven.

## Typography

All text uses locally bundled IBM Plex Mono, weight 500. The two Inter headings
in Figma are normalized to that shared family.

| Role | Size | Line height | Token |
| --- | --- | --- | --- |
| Column heading | 10px | 14px | --text-column |
| Telemetry / counts / footer | 11px | 16px (footer 15px) | --text-meta |
| Status / network / group | 12px | 18px | --text-status |
| Control / event / node plate | 13px | 18px | --text-control |
| Host / app name | 14px | 20px | --text-item |
| Detail subsection heading | 15px | 21px | --text-subheading |
| Brand / section heading | 20px | 28px | --text-heading |

Column headings are uppercase with 0.7px tracking; environment groups use 1px
tracking. The lab name is the page h1, Apps is h2. Other labels retain their actual
roles, avoiding visual styles that force an incorrect heading level.

## Geometry

- Radius: 2px, with circles retained for meaningful health indicators.
- Node plate / app status / event row: 32px.
- Navigation / toolbar button: 40px (normalizes the Figma 40–42px discrepancy).
- App row: 52px. Host row: 64px. Column header: 42px.
- Page inset: 48px horizontal / 32px vertical at the 1600px reference width.
- Section gap: 26px; row gaps: 2px for apps, 8px for hosts and events.
- Shared spacing steps: 4, 8, 12, 16, 24px.
- Directories use common column definitions, with horizontal scrolling on narrower
  widths; mobile redesign is outside this reference.

## Node identity

A node plate uses the theme foreground as its block and the raised-surface color
for its text and shape. The version-6 generator is reused from bauhaus-avatar-lab;
its geometry is unchanged. The full trimmed NFC name seeds the shape, independently
of colors. Repeated references to one node share one shape; different names can
produce the same abbreviation, so the abbreviation is not a unique identifier.

Current abbreviation convention follows the frame: the first two consonants and
the final alphanumeric character, uppercase. Ignore separators and fold Latin
accents; treat Y as a consonant. Example: node-01 → ND1, Everforest → VRT.
If fewer than two consonants exist, fill with the name's leading alphanumeric
characters; a single character repeats. Empty labels display an em dash. This
convention is provisional pending the user's naming-rule clarification.

## Components and preview behavior

Primitives: Node Plate, Health Mark, Status Badge, Telemetry, Asset Icon; existing
UI Button gains the surface variant and Resource Track gains the compact size.
Patterns: Dashboard Navigation, Lab Event Log, Host Directory, App Directory,
Lab Dashboard. The page contains domain content, so it is a Pattern, not a Layout.

All controls emit actions for an application to handle. The dashboard opens the
existing searchable command palette. Stories demonstrate fixture data and states;
no server calls or real infrastructure actions are performed. Functional icons use
Tabler filled variants, with the directional arrow as the small stroke glyph used
by the reference. Imported app logos and the brand are preserved. Loose floating
icons in the mockup are incorporated into the relevant control positions rather
than pinned over unrelated content. The overlapping, obscured uptime text in the
source is not included in the composed screen.

The subsequent detail-screen controls and API mapping are documented in
[detail workspace](detail-workspace.md).
