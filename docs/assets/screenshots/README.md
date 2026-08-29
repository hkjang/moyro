# Verified product screenshots

This directory contains 25 real moyro v0.1.0 screenshots captured by the
Playwright release acceptance test in `webapp/e2e/product-pages.spec.ts`.
The test starts a disposable PostgreSQL database and the production Docker
image, seeds synthetic data, checks each routed page before and after refresh,
fails on browser or same-origin HTTP errors, and then writes the JPEG files.

The set covers login, workspace, saved and scheduled posts, the profile menu,
personal settings, service administration, and representative 430×932 mobile
views. It contains no production account data or secrets.

Regenerate and verify the complete set with:

```bash
bash scripts/verify-product-ui.sh moyro:v0.1.0 docs/assets/screenshots
node scripts/verify-pages.mjs
```

Do not replace these files with mocks or marketing composites. The release and
Pages workflows enforce that every expected JPEG is complete and referenced by
the published HTML.
