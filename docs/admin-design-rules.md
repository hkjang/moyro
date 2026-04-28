# Admin Design Rules

Status date: 2026-04-28

The Admin Console follows a "Well & Good" visual rule set: calm, clear,
polished, and operational. It should feel like a professional control room, not
a chat overlay or marketing page.

## Visual Principles

- Calm base: light operational surfaces with low-noise contrast.
- Fresh accents: restrained green, indigo, red, amber, and blue status tokens.
- Clear hierarchy: scope header, navigation, page title, toolbar, data body.
- Table first: admin data should scan quickly before it decorates.
- Soft depth: shadows separate working planes, not decorative cards.
- Consistent states: active, inactive, warning, danger, and info use shared
  badge/button tokens.
- Accessible controls: focus rings, readable contrast, stable row and button
  dimensions.

## Current Tokens

Admin-specific CSS variables live under `.admin-page`:

- `--admin-bg`, `--admin-surface`, `--admin-surface-soft`
- `--admin-ink`, `--admin-muted`, `--admin-line`
- `--admin-accent`, `--admin-accent-2`
- `--admin-success`, `--admin-warning`, `--admin-danger`, `--admin-info`

The chat workspace keeps its existing dark theme; Admin uses scoped overrides so
the two products can evolve independently.
