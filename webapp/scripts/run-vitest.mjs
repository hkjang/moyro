#!/usr/bin/env node
// Run vitest on the Node that npm itself is running on.
//
// When npm runs a script it builds PATH by prepending `node_modules/.bin` from the
// package directory *and from every ancestor directory* up to the filesystem root
// (@npmcli/run-script/lib/set-path.js). A `node` package installed anywhere above the
// checkout — `~/node_modules/node`, say — therefore outranks the Node that nvm or
// actions/setup-node selected, so `vitest` and the worker processes it forks all run on
// that interpreter instead. The suite does not merely warn about this: jsdom pulls in
// undici, which builds a `CacheStorage` at import time using
// `worker_threads.markAsUncloneable`, so on a Node without that API every jsdom test
// file fails to start with "webidl.util.markAsUncloneable is not a function" and the
// failure reads as a broken dependency rather than a shadowed interpreter.
//
// npm publishes the interpreter it is running on as `npm_node_execpath`, which is the
// Node that was actually selected, so prefer it over whatever PATH resolved. Fall back to
// the current interpreter when vitest is started outside npm.
import { spawn } from "node:child_process";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";

// Resolve through package.json so this does not depend on vitest's exports map.
const vitestEntry = join(
  dirname(createRequire(import.meta.url).resolve("vitest/package.json")),
  "vitest.mjs",
);

const nodeExecPath = process.env.npm_node_execpath || process.execPath;

const child = spawn(nodeExecPath, [vitestEntry, ...process.argv.slice(2)], {
  stdio: "inherit",
});

child.on("error", (error) => {
  console.error(`failed to start vitest with ${nodeExecPath}:`, error);
  process.exit(1);
});

child.on("exit", (code, signal) => {
  if (signal) {
    process.kill(process.pid, signal);
    return;
  }
  process.exit(code ?? 1);
});
