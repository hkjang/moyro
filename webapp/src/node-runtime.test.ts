import { describe, expect, it } from "vitest";

import jsdomPackageJson from "jsdom/package.json";
import packageJson from "../package.json";

// `process` is deliberately outside this project's type scope (see tsconfig.test.ts), so
// reach the running interpreter through `globalThis` rather than widening `types`.
const runtime = (globalThis as { process?: { version?: string } }).process;
const runningMajor = Number(/^v(\d+)\./.exec(runtime?.version ?? "")?.[1]);

// npm builds a script's PATH by prepending `node_modules/.bin` from the package directory
// *and from every ancestor directory* up to the filesystem root. A `node` package
// installed anywhere above the checkout — `~/node_modules/node`, say — therefore outranks
// the Node that nvm or actions/setup-node selected, and `npm test` quietly runs the suite
// on that interpreter instead. jsdom needs a Node far newer than such a stray pin usually
// is, and when it does not get one it dies while importing undici
// ("webidl.util.markAsUncloneable is not a function") before a single jsdom test file
// starts, which reads as a broken dependency rather than a shadowed interpreter. These
// assertions name the real cause instead, and keep the launcher that pins it wired up.
describe("test runtime", () => {
  it("reports the interpreter it is running on", () => {
    expect(runningMajor).toBeGreaterThan(0);
  });

  // A necessary condition rather than full semver range satisfaction: the lowest major
  // jsdom accepts is read from jsdom itself, so this keeps tracking the dependency.
  it("runs on a Node new enough for the jsdom this project installs", () => {
    const lowestSupportedMajor = Math.min(
      ...jsdomPackageJson.engines.node
        .split("||")
        .map((clause) => Number(/(\d+)\./.exec(clause)?.[1])),
    );

    expect(lowestSupportedMajor).toBeGreaterThan(0);
    expect(runningMajor).toBeGreaterThanOrEqual(lowestSupportedMajor);
  });

  it("is launched through the runner that pins the interpreter", () => {
    expect(packageJson.scripts.test).toContain("scripts/run-vitest.mjs");
  });
});
