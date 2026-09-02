# Moyro Flow Design Rules

Status date: 2026-09-02

Moyro keeps familiar channel, message, thread, and keyboard behavior at its
Mattermost-compatible boundary. Its product interface is independently
designed as an enterprise workflow hub for reading conversations, making
decisions, and executing approved work.

## Product Principles

- **Calm:** use low-saturation surfaces, light borders, and restrained motion
  so long work sessions remain readable.
- **Context:** keep a conversation's thread, AI summary, files, links, tasks,
  decisions, and channel information near the source instead of creating
  disconnected feature islands.
- **Flow:** move from message to reminder, approval, or external action without
  hiding the originating actor and channel.
- **Trust:** show whether data is live, unavailable, permission-limited, or only
  prepared. AI output identifies its scope and links only to source messages
  actually supplied to the model.
- **Focus:** Today, Inbox, and My Work separate items requiring attention from
  ordinary channel browsing.
- **Familiarity:** channel selection, flat message rows, threads, reactions,
  composer shortcuts, and Mattermost-shaped API behavior remain predictable.

## Design-token ownership

`webapp/src/theme/flowTokens.ts` is the TypeScript source for MUI palettes and
semantic roles. `webapp/src/styles/tokens.css` exposes the corresponding CSS
custom properties for legacy and extracted workspace styles. Components must
use semantic roles rather than inventing screen-local brand colors.

Core roles are:

- brand `#3157D5`, brand hover `#203E9F`
- navigation `#15213D`
- AI `#6D55C5`
- automation/MCP `#0F766E`
- approval attention `#B76E00`
- success `#218358`, danger `#C2414B`
- page `#F6F7F9`, surface `#FFFFFF`, border `#DFE3EA`
- text `#182033`, secondary text `#667085`

`ThemePreferenceProvider` is the only runtime owner of light, dark, or system
mode. It synchronizes MUI color schemes, the first-paint storage key, and the
Mattermost-shaped `display_settings/theme` preference. Feature components must
not write `html[data-theme]` directly.

## Layout rules

- Desktop uses a 60–68 px global rail, a 264–288 px workspace sidebar, a
  flexible message region, and an optional 380–440 px context panel.
- The global rail changes product scope; the context sidebar changes team,
  channel, or direct conversation inside the workspace.
- Messages remain flat rows rather than speech bubbles. Hover and focus expose
  compact communication actions; workflow actions live in the more menu.
- Consecutive posts by one author within five minutes group under a single
  header; a day separator and the "새 메시지" marker always start a new group.
  The channel opens on the newest message and offers "최신 메시지로" once the
  reader scrolls up, rather than moving the view under them.
- Every timestamp comes from `webapp/src/lib/time.ts`; no surface formats time
  on its own.
- Every shortcut the workspace implements is listed in the `?` help dialog;
  a shortcut that is not listed there does not exist.
- The right side is one context panel with thread, summary, files, and channel
  information—not several competing drawers.
- My Work owns saved messages, scheduled posts, and reminders. They must not be
  styled or routed as pseudo-channels; the workspace sidebar no longer lists
  them.
- Approval is a global work surface, not a personal-setting subsection.
- Admin entry pages lead with actual operating state and unknown/error states,
  not only navigation cards or optimistic green indicators.

## Responsive and accessibility rules

- Mobile shows Today, Workspace, Inbox, Search, and More in a fixed bottom
  navigation. Only one primary pane is visible at a time.
- Mobile workspace navigation and context surfaces behave as modal, full-screen
  tasks with Escape handling, focus containment, and focus return.
- Desktop targets are at least 36 px; mobile touch targets are at least 44 px.
- Every state combines text or icon shape with color and meets WCAG AA contrast.
- Channel selection, search, message composition, context tabs, and approval
  actions must be operable by keyboard. Ordinary message rows are not all Tab
  stops; exact-source navigation may focus a row programmatically.
- Reduced-motion and forced-colors preferences are honored. At 200% zoom, core
  work remains available without horizontal page scrolling.

## Compatibility boundary

Visual similarity to Mattermost is not a requirement. Compatibility is tested
at REST/JSON route shape, WebSocket events, integrations, and familiar client
workflow behavior. Decorative gradients, generic AI sparkle treatments, false
capabilities, and copied cloud-only screens are outside the Moyro Flow design
language.
