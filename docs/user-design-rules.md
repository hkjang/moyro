# User Design Rules

Status date: 2026-04-28

The user-facing chat workspace follows the same "Well & Good" design direction
as Admin, but with a softer daily-use tone. It should feel calm, readable, and
pleasant for long chat sessions.

## Visual Principles

- Warm clean canvas: light paper-like chat workspace scoped to `.chat-shell`.
- Reading first: messages use soft depth, compact metadata, and generous line
  height.
- Calm navigation: sidebar rows are quiet by default and clear when active.
- Focused composition: the composer is visually stable and easy to return to.
- Restrained accents: green for primary action, indigo for secondary depth,
  coral/red only for attention or destructive states.
- Scoped implementation: user chat styling must not leak into Admin or Login.

## Current Tokens

User-specific CSS variables live under `.chat-shell`:

- `--chat-bg`, `--chat-bg-soft`
- `--chat-surface`, `--chat-surface-solid`, `--chat-surface-soft`
- `--chat-ink`, `--chat-muted`, `--chat-line`
- `--chat-accent`, `--chat-accent-2`, `--chat-coral`
- `--chat-warning`, `--chat-danger`

The chat workspace remaps existing shared variables such as `--bg`, `--fg`,
`--muted`, `--accent`, and input colors inside `.chat-shell`, so existing
components inherit the cleaner visual language without JSX churn.
