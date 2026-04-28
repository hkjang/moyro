# User Design Rules

Status date: 2026-04-29

The user-facing chat workspace follows a Mattermost-aligned product direction:
dark channel navigation, white message surfaces, compact headers, predictable
RHS behavior, and low-decoration density for long chat sessions.

## Visual Principles

- Mattermost-like shell: dark channel sidebar, white chat/RHS surfaces, and
  clear one-pixel panel borders scoped to `.chat-shell`.
- Reading first: messages use soft depth, compact metadata, and generous line
  height.
- Calm navigation: sidebar rows are quiet by default and clear when active.
- Daily panels first: left navigation and RHS thread panels should prioritize
  scan speed, stable widths, sticky account controls, and readable badges.
- Focused composition: the composer is visually stable and easy to return to.
- Restrained accents: Mattermost-style blue for primary action, red only for
  mentions, destructive states, or urgent attention.
- Scoped implementation: user chat styling must not leak into Admin or Login.

## Current Tokens

User-specific CSS variables live under `.chat-shell`:

- `--chat-bg`, `--chat-bg-soft`
- `--chat-surface`, `--chat-surface-solid`, `--chat-surface-soft`
- `--chat-ink`, `--chat-muted`, `--chat-line`
- `--chat-accent`, `--chat-accent-2`, `--chat-coral`
- `--chat-sidebar`, `--chat-sidebar-2`, `--chat-sidebar-text`
- `--chat-warning`, `--chat-danger`

The chat workspace remaps existing shared variables such as `--bg`, `--fg`,
`--muted`, `--accent`, and input colors inside `.chat-shell`, so existing
components inherit the cleaner visual language without JSX churn.

## Panel Rules

- Left sidebar uses a stable desktop width and grouped section dividers so
  teams, saved/scheduled shortcuts, favorites, channels, and DMs scan quickly.
- Active sidebar rows must be stronger than hover rows and include both color
  and shape cues for accessibility.
- Sidebar account/status controls stay visually anchored at the bottom on
  desktop, but become static on tablet/mobile to avoid trapping scroll space.
- Unread, mention, scheduled, and favorite affordances must remain compact and
  legible without resizing rows.
- RHS thread panels use full-width message rows, a clear root/replies divider,
  a compact sticky-feeling header, and a stable composer area.
- Mobile and tablet layouts keep the sidebar as a top navigation pane and turn
  RHS into a focused overlay rather than shrinking message content too far.

## Mattermost Alignment Rules

- Avoid decorative gradients, heavy shadows, and large rounded cards in the
  user chat shell; Mattermost's current web UI reads as product-dense and
  utility-first.
- Main message lists should behave like rows, not isolated speech bubbles.
  Hover may reveal a light row highlight, but default messages stay flat.
- RHS thread panels should feel like a first-class work panel: compact header,
  visible root message, simple reply divider, and stable composer.
- Sidebar affordances should match Mattermost's channel workflow: favorites,
  unread counts, direct messages, saved/scheduled shortcuts, and quick switcher
  must remain scannable without visual noise.
