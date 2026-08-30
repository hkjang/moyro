# Verified product screenshots

The moyro v0.2.1 release gallery contains 46 real screenshots produced by the
Playwright acceptance scenarios in `webapp/e2e/product-pages.spec.ts` and
`webapp/e2e/plugin-compatibility.spec.ts`. The scenarios start a disposable
PostgreSQL database and the production Docker image, seed synthetic data, check
the routed pages and named plugin workflows, fail on browser or same-origin
HTTP errors, and then write the JPEG files.

The catalog covers 29 authenticated desktop routes (thirteen Moyro Flow views, the
channel workspace, six personal-settings routes, and nine admin routes), a
desktop workspace-context state, the public login and profile menu states, and
four plugin compatibility states plus ten representative 430×932 mobile
views. The plugin additions show all four compatible archive rows, a dedicated
EchoSummary admin settings route, Langflow's RHS/history workflow, and EchoSummary
user settings. The Flow and context set includes Today, durable activity updates,
conversation and approval inboxes, five My Work states including tasks and
decisions, two approval-center states, AI assistant, global search, the desktop
context panel, and mobile Flow/context/action views. `scripts/verify-pages.mjs` compares the catalog with both
application navigation definitions so a newly added settings or admin route
cannot silently ship without a capture. It contains no production account data
or secrets.

Regenerate and verify the complete set with:

```bash
MOYRO_PLUGIN_FIXTURE_DIR=/absolute/path/to/plugin-archives \
  bash scripts/verify-product-ui.sh moyro:v0.2.1 docs/assets/screenshots
node scripts/verify-pages.mjs
```

`MOYRO_PLUGIN_FIXTURE_DIR` must contain exactly one release archive for each
of `com.mattermost.botman-*`, `com.hkjang.mattermost-chatdump-plugin-*`,
`com.mattermost.echosummary-*`, and `com.mattermost.langflow-*`. Omitting the
variable intentionally skips the real-plugin browser scenarios, so it is not a
complete release verification. CI and the release workflow provide this
directory from their fixture-build step.

The capture directory may be relative or absolute; the wrapper canonicalizes
it before Playwright runs. CI can use a temporary directory when it only needs
to verify the scenario without replacing the checked-in release gallery.

Do not replace these files with mocks or marketing composites. The release and
Pages workflows enforce that every expected JPEG is complete and referenced by
the published HTML.
