import { describe, expect, it } from "vitest";

import packageJson from "../package.json";
import nodeTsconfig from "../tsconfig.node.json";
import tsconfig from "../tsconfig.json";

// `compilerOptions.types` pins which global type packages TypeScript loads. Without it,
// tsc pulls in every `node_modules/@types/*` it can find, walking up past the webapp
// directory into ancestor directories that have nothing to do with this project. When an
// `@types/node` turns up in one of those, DOM globals such as `window.setInterval` start
// resolving to Node's `Timeout` return type and typechecking fails on code that never
// changed. Keep the list explicit so a checkout typechecks the same way everywhere.
describe("type scope", () => {
  const types: unknown = tsconfig.compilerOptions.types;

  it("is pinned to the packages this project actually uses", () => {
    expect(types).toEqual(["vite/client"]);
  });

  it("does not pull in Node globals", () => {
    expect(types).not.toContain("node");
  });
});

// The app config covers `src` only, which once left the Playwright specs and the two
// build configs outside every check we run — Playwright transpiles specs without
// typechecking them, so a renamed helper or a mistyped `page.evaluate` callback only
// surfaced when the browser job failed late in CI. `tsconfig.node.json` covers exactly
// that leftover set; these assertions keep it wired up and keep its Node type scope from
// leaking back into the app project.
describe("node-side type scope", () => {
  it("covers the files the app project leaves out", () => {
    expect(nodeTsconfig.include).toEqual(["e2e", "vite.config.ts", "playwright.config.ts"]);
  });

  it("takes Node globals instead of the browser-only app scope", () => {
    expect(nodeTsconfig.compilerOptions.types).toEqual(["node"]);
    expect(nodeTsconfig.extends).toBe("./tsconfig.json");
  });

  it("declares the @types/node it depends on rather than inheriting a stray one", () => {
    expect(packageJson.devDependencies).toHaveProperty("@types/node");
  });

  it("is actually run by the typecheck script", () => {
    expect(packageJson.scripts.typecheck).toContain("-p tsconfig.node.json");
  });
});
