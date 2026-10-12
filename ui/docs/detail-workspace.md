# Detail workspace

Reference: [Figma detail frame 1700:20873](https://www.figma.com/design/yJzrnkHhurVIrwIpq5UINP/FloatLab?node-id=1700-20873).

## Structure and responsiveness

`SplitView` is a content-agnostic Layout. `DetailPanel` is the shared Pattern for
a titled screen with identity, close button, initial keyboard focus and Escape to
close. `DashboardWorkspace` composes the existing dashboard and host detail, add
host, add app and alert settings examples. Close returns focus to the opening
control. The global command palette also opens creation/settings panels.

At the 1440px reference width, the inner columns are approximately 421px / 916px
with an 18px gap. The sidebar's directories respond to their container width,
keeping identity, state, host and action controls visible while hiding secondary
columns. At 900px and below, the detail view takes the available width; closing
returns to the dashboard. The dashboard remains mounted while detail is open.

Panel entry and exit use a 280ms ease-out slide and 200ms opacity transition.
Column sizing transitions alongside it. Reduced-motion preferences remove these
transitions. The frame's two overlapping host-form examples and duplicate Storage
tabs are normalized to one form and one Storage tab.

## Shared controls and tokens

- Input: existing `UiInput` gains a `field` variant, 36px tall with raised surface.
- Switch: explicit On/Off labels, 36×20px square-corner track, 14px thumb;
  accent for on, recessed surface for off. Native button keyboard behavior.
- Select: native single or multiple selection, 36px minimum height, keyboard
  support and theme colors. The multiple example is an accessible list rather
  than a custom popup matching the mockup's collapsed summary.
- Tabs: 40px tall; accent background and canvas text when active; arrow keys,
  Home/End, roving tab focus, disabled states and linked tab panel.
- Form rows: 60px minimum height, 16px gap, labels and controls stack on narrow
  screens. JSON editors are used for nested API objects/arrays; Compose is a
  multiline text editor.
- Detail sections use a shared 15px / 21px subheading token. Panel and primary
  headings use 20px / 28px. All use IBM Plex Mono 500 and the shared 2px radius.
- `--surface-recessed` maps directly to Omarchy `dark_background`: Everforest
  #21272C. Canvas, raised controls, text and accent keep the existing roles.

## Core API mapping

Source: `floatlab-core/api/openapi/{nodes,config,stacks}.yaml`, inspected at commit
`f2549ac37351e9731a17e17916de96869c796432` (working-tree schemas are the source).

`pnpm forms:sync` generates checked-in `src/forms/core-forms.json`. The default
source is the local `api/openapi` directory in the core repository; pass an
absolute OpenAPI directory to the script for another checkout. No API connection
is needed to run Storybook. Regenerate after changing the core contract.

| Screen | Schema | API operation |
| --- | --- | --- |
| Host settings | NodeUpdate | PUT /nodes/{id} |
| Add host | NodeCreate | POST /nodes |
| Alert settings example | AlertRuleCreate | POST /alert-rules |
| Add app | StackCreate | POST /stacks |

Labels derive from field names. Required fields, local references, descriptions,
enum options and defaults derive from the schema. Deprecated properties are
excluded: new apps use `name` and `compose_file`, with placement defined through
Compose x-fl-* extensions. No placement fields are invented in this UI.

`ApiForm` emits only declared, populated fields, parsing structured editors as
JSON. Native form validation checks required inputs. This is a small renderer for
the selected contracts, not a complete OpenAPI validation engine. Full nested
validation, server errors, resource-option fetching and real submission are
future integration work. The sync step takes contract changes into the local
stories; it does not discover a running server's API dynamically.

Automatic updates, per-host resource-alert switches and alert-category selection
from the mockup are not NodeUpdate fields. Switches are demonstrated in a separate
"Design preview" form; multiple categories appear in the Select primitive story.
These values never enter a node update payload. Host pause/restart/shut-down
buttons are design actions only: the inspected node API does not declare those
operations. Logs remain a clearly labelled disconnected local preview.

All save/create controls emit local events; no backend changes are made. The
existing fixtures remain illustrative, including their original addresses and
statuses. Connecting live data should map actual core status enums and IPv6 node
addresses explicitly.
