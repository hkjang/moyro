import { describe, expect, it } from "vitest";

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
