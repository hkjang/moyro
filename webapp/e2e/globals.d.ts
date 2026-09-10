// Globals the running app installs on `window`, redeclared for the specs.
//
// The app's own declaration lives in `src/plugins/runtime.ts`, but the code a
// spec passes to `page.evaluate` is typechecked here in the e2e project, which
// deliberately does not pull `src` in — importing the plugin runtime for its
// types alone would drag the Redux store into a Playwright file. Keep this in
// sync with the `declare global` block in `src/plugins/runtime.ts`, and add
// entries only for the properties a spec actually reads.
interface Window {
  /**
   * The pre-plugin `fetch`, stashed by `installAuthenticatedPluginFetch` before
   * it swaps in the facade that rewrites credentials from the Redux session.
   * Absent until the plugin runtime has started.
   */
  __moyro_plugin_fetch_original__?: typeof fetch;
}
