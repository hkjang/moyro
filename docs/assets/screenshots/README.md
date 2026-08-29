# Verified product screenshots

This directory contains 25 real moyro v0.1.1 screenshots captured by the
Playwright release acceptance test in `webapp/e2e/product-pages.spec.ts`.
The test starts a disposable PostgreSQL database and the production Docker
image, seeds synthetic data, checks each routed page before and after refresh,
fails on browser or same-origin HTTP errors, and then writes the JPEG files.

The catalog covers 19 authenticated desktop routes (three workspace views,
every eight-item personal-settings route, and every eight-item admin route),
the public login and profile menu states, and four representative 430×932
mobile views. `scripts/verify-pages.mjs` compares the catalog with both
application navigation definitions so a newly added settings or admin route
cannot silently ship without a capture. It contains no production account data
or secrets.

Regenerate and verify the complete set with:

```bash
bash scripts/verify-product-ui.sh moyro:v0.1.1 docs/assets/screenshots
node scripts/verify-pages.mjs
```

The capture directory may be relative or absolute; the wrapper canonicalizes
it before Playwright runs. CI can use a temporary directory when it only needs
to verify the scenario without replacing the checked-in release gallery.

Do not replace these files with mocks or marketing composites. The release and
Pages workflows enforce that every expected JPEG is complete and referenced by
the published HTML.
