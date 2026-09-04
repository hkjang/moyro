#!/usr/bin/env node

import { existsSync, readFileSync, readdirSync, statSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const docs = join(root, "docs");
const screenshots = join(docs, "assets", "screenshots");
const expectedScreenshots = [
  "login.jpg",
  "today.jpg",
  "inbox-updates.jpg",
  "inbox-conversations.jpg",
  "inbox-approvals.jpg",
  "my-work-tasks.jpg",
  "my-work-decisions.jpg",
  "my-work-saved.jpg",
  "my-work-scheduled.jpg",
  "my-work-reminders.jpg",
  "approvals-mine.jpg",
  "approvals-review.jpg",
  "ai-assistant.jpg",
  "global-search.jpg",
  "workspace-channel.jpg",
  "workspace-context-info.jpg",
  "plugin-langflow-rhs.jpg",
  "workspace-profile-menu.jpg",
  "settings-profile.jpg",
  "settings-appearance.jpg",
  "settings-notifications.jpg",
  "settings-sessions.jpg",
  "settings-keys.jpg",
  "settings-ai.jpg",
  "settings-plugin-echosummary.jpg",
  "admin-overview.jpg",
  "admin-site.jpg",
  "admin-keycloak.jpg",
  "admin-ai.jpg",
  "admin-key-policy.jpg",
  "admin-mcp.jpg",
  "admin-plugins.jpg",
  "admin-plugins-compatible.jpg",
  "admin-plugin-echosummary.jpg",
  "admin-approval.jpg",
  "admin-operations.jpg",
  "mobile-login.jpg",
  "mobile-workspace.jpg",
  "mobile-message-actions.jpg",
  "mobile-workspace-context.jpg",
  "mobile-today.jpg",
  "mobile-inbox.jpg",
  "mobile-my-work.jpg",
  "mobile-approvals.jpg",
  "mobile-settings-profile.jpg",
  "mobile-admin-site.jpg",
];
const expectedScreenshotDimensions = new Map(expectedScreenshots.map((name) => [
  name,
  name.startsWith("mobile-") ? { width: 430, height: 932 } : { width: 1440, height: 1000 },
]));
const productCaptureSpec = readFileSync(join(root, "webapp", "e2e", "product-pages.spec.ts"), "utf8");
const pluginCaptureSpec = readFileSync(join(root, "webapp", "e2e", "plugin-compatibility.spec.ts"), "utf8");
const captureSpec = `${productCaptureSpec}\n${pluginCaptureSpec}`;
const capturedScreenshotNames = new Set(
  [...captureSpec.matchAll(/["'`]([a-z0-9][a-z0-9-]*\.jpg)["'`]/g)].map((match) => match[1]),
);
const routedCatalogStart = productCaptureSpec.indexOf("const routedPages");
const routedCatalogEnd = productCaptureSpec.indexOf("for (const route of routedPages)", routedCatalogStart);
const routedCatalog = routedCatalogStart >= 0 && routedCatalogEnd > routedCatalogStart
  ? productCaptureSpec.slice(routedCatalogStart, routedCatalogEnd)
  : "";
const capturedNavigationRoutes = new Set(
  [...routedCatalog.matchAll(/path:\s*\(\)\s*=>\s*["'](\/(?:settings|admin)\/[^"']+)["']/g)]
    .map((match) => match[1]),
);
const navigationRoutes = new Set();
for (const source of [
  join(root, "webapp", "src", "layouts", "PersonalSettingsLayout.tsx"),
  join(root, "webapp", "src", "layouts", "AdminLayout.tsx"),
]) {
  const contents = readFileSync(source, "utf8");
  for (const match of contents.matchAll(/to:\s*["'](\/(?:settings|admin)\/[^"']+)["']/g)) {
    navigationRoutes.add(match[1]);
  }
}

const failures = [];
const htmlFiles = walk(docs).filter((file) => file.endsWith(".html"));
const htmlByFile = new Map(htmlFiles.map((file) => [file, readFileSync(file, "utf8")]));
const allHTML = [...htmlByFile.values()].join("\n");
const actualScreenshotDimensions = new Map();
const expectedBrandPNGs = new Map([
  ["favicon-16.png", { width: 16, height: 16 }],
  ["favicon-32.png", { width: 32, height: 32 }],
  ["apple-touch-icon.png", { width: 180, height: 180 }],
  ["icon-192.png", { width: 192, height: 192 }],
  ["icon-512.png", { width: 512, height: 512 }],
  ["maskable-icon-512.png", { width: 512, height: 512 }],
  ["moyro-social-card.png", { width: 1200, height: 630 }],
]);

for (const name of readdirSync(screenshots).filter((entry) => entry.endsWith(".jpg"))) {
  if (!expectedScreenshotDimensions.has(name)) {
    failures.push(`stale or uncatalogued screenshot: docs/assets/screenshots/${name}`);
  }
}

for (const name of expectedScreenshots) {
  const file = join(screenshots, name);
  if (!existsSync(file)) {
    failures.push(`missing screenshot: docs/assets/screenshots/${name}`);
    continue;
  }
  const bytes = readFileSync(file);
  if (bytes.length < 20_000) failures.push(`screenshot is unexpectedly small: ${name} (${bytes.length} bytes)`);
  if (bytes[0] !== 0xff || bytes[1] !== 0xd8 || bytes.at(-2) !== 0xff || bytes.at(-1) !== 0xd9) {
    failures.push(`screenshot is not a complete JPEG: ${name}`);
  }
  try {
    const actual = readJPEGDimensions(bytes);
    const expected = expectedScreenshotDimensions.get(name);
    actualScreenshotDimensions.set(name, actual);
    if (actual.width !== expected.width || actual.height !== expected.height) {
      failures.push(
        `screenshot dimensions are ${actual.width}x${actual.height}, want ${expected.width}x${expected.height}: ${name}`,
      );
    }
  } catch (error) {
    failures.push(`cannot read JPEG dimensions for ${name}: ${error.message}`);
  }
  if (!allHTML.includes(`screenshots/${name}`)) failures.push(`screenshot is not used by any page: ${name}`);
  if (!capturedScreenshotNames.has(name)) failures.push(`screenshot is not produced by the browser capture specs: ${name}`);
}

for (const name of capturedScreenshotNames) {
  if (!expectedScreenshotDimensions.has(name)) {
    failures.push(`browser capture specs write an uncatalogued screenshot: ${name}`);
  }
}

if (!routedCatalog) {
  failures.push("cannot locate the routedPages capture catalog in product-pages.spec.ts");
}
for (const route of navigationRoutes) {
  if (!capturedNavigationRoutes.has(route)) {
    failures.push(`navigable settings/admin route has no routed screenshot: ${route}`);
  }
}
for (const route of capturedNavigationRoutes) {
  if (!navigationRoutes.has(route)) {
    failures.push(`routed screenshot no longer has a settings/admin navigation entry: ${route}`);
  }
}

for (const [name, expected] of expectedBrandPNGs) {
  const file = join(docs, "assets", "brand", name);
  if (!existsSync(file)) {
    failures.push(`missing brand asset: docs/assets/brand/${name}`);
    continue;
  }
  try {
    const actual = readPNGDimensions(readFileSync(file));
    if (actual.width !== expected.width || actual.height !== expected.height) {
      failures.push(`brand asset dimensions are ${actual.width}x${actual.height}, want ${expected.width}x${expected.height}: ${name}`);
    }
  } catch (error) {
    failures.push(`cannot read brand PNG dimensions for ${name}: ${error.message}`);
  }
}

for (const relativePath of [
  "favicon.svg",
  "favicon.ico",
  "apple-touch-icon.png",
  "site.webmanifest",
  "assets/brand/moyro-mark.svg",
  "assets/brand/moyro-wordmark.svg",
]) {
  if (!existsSync(join(docs, relativePath))) failures.push(`missing brand asset: docs/${relativePath}`);
}

for (const [file, html] of htmlByFile) {
  if (/placeholder|자리표시자|실제 캡처 대기|TODO_SCREENSHOT/i.test(html)) {
    failures.push(`placeholder copy remains in ${relative(root, file)}`);
  }
  if (/<meta\s+name=["']keywords["']/i.test(html)) {
    failures.push(`obsolete meta keywords remain in ${relative(root, file)}`);
  }
  for (const marker of [
    'rel="icon"',
    'rel="apple-touch-icon"',
    'rel="manifest"',
    'assets/brand/moyro-social-card.png',
    'class="brand-logo"',
  ]) {
    if (!html.includes(marker)) failures.push(`brand/metadata marker is missing in ${relative(root, file)}: ${marker}`);
  }
  for (const match of html.matchAll(/<script\b[^>]*type=["']application\/ld\+json["'][^>]*>([\s\S]*?)<\/script>/gi)) {
    try {
      JSON.parse(match[1]);
    } catch (error) {
      failures.push(`invalid JSON-LD in ${relative(root, file)}: ${error.message}`);
    }
  }
  for (const match of html.matchAll(/(?:href|src)=["']([^"'#]+)(?:#[^"']*)?["']/gi)) {
    const target = match[1].trim();
    if (/^(?:https?:|mailto:|tel:|data:|javascript:)/i.test(target) || target.startsWith("//")) continue;
    const withoutQuery = target.split("?")[0];
    const resolved = withoutQuery.startsWith("/")
      ? join(docs, withoutQuery.replace(/^\/+/, ""))
      : resolve(dirname(file), withoutQuery);
    const candidate = withoutQuery.endsWith("/") ? join(resolved, "index.html") : resolved;
    if (!existsSync(candidate)) {
      failures.push(`broken local asset/link in ${relative(root, file)}: ${target}`);
    }
  }
  for (const match of html.matchAll(/<img\b[^>]*>/gi)) {
    const attributes = Object.fromEntries(
      [...match[0].matchAll(/([:\w-]+)\s*=\s*(?:"([^"]*)"|'([^']*)')/g)]
        .map((attribute) => [attribute[1].toLowerCase(), attribute[2] ?? attribute[3] ?? ""]),
    );
    const source = attributes.src?.split(/[?#]/, 1)[0] ?? "";
    const name = source.split("/").at(-1);
    if (!expectedScreenshotDimensions.has(name)) continue;

    const declared = { width: Number(attributes.width), height: Number(attributes.height) };
    const expected = expectedScreenshotDimensions.get(name);
    if (!Number.isInteger(declared.width) || !Number.isInteger(declared.height) ||
        declared.width !== expected.width || declared.height !== expected.height) {
      failures.push(
        `HTML dimensions for ${name} are ${attributes.width ?? "missing"}x${attributes.height ?? "missing"}, ` +
        `want ${expected.width}x${expected.height} in ${relative(root, file)}`,
      );
    }
    const actual = actualScreenshotDimensions.get(name);
    if (actual && (declared.width !== actual.width || declared.height !== actual.height)) {
      failures.push(
        `HTML dimensions ${declared.width}x${declared.height} do not match JPEG ` +
        `${actual.width}x${actual.height} for ${name} in ${relative(root, file)}`,
      );
    }
  }
}

const index = readFileSync(join(docs, "index.html"), "utf8");
for (const required of [
  '<link rel="canonical"',
  'property="og:image"',
  'name="twitter:image"',
  'application/ld+json',
  'href="screens.html"',
  'href="guides/user-guide.html"',
  'href="guides/admin-guide.html"',
]) {
  if (!index.includes(required)) failures.push(`index.html is missing required SEO/navigation marker: ${required}`);
}

for (const releasePage of [
  join(docs, "index.html"),
  join(docs, "guides", "user-guide.html"),
  join(docs, "guides", "admin-guide.html"),
  join(docs, "guides", "offline-deployment.html"),
]) {
  const html = htmlByFile.get(releasePage) ?? "";
  if (!html.includes("v0.2.15")) failures.push(`v0.2.15 release marker is missing in ${relative(root, releasePage)}`);
  if (/v0\.1\.[01]/.test(html)) failures.push(`stale v0.1.x release marker remains in ${relative(root, releasePage)}`);
  if (!html.includes('"dateModified": "2026-09-04"')) {
    failures.push(`2026-09-04 dateModified is missing in ${relative(root, releasePage)}`);
  }
}

const screensPage = join(docs, "screens.html");
const screensHTML = htmlByFile.get(screensPage) ?? "";
if (!screensHTML.includes("v0.2.1") || !screensHTML.includes('"dateModified": "2026-08-30"')) {
  failures.push("screens.html must retain the version and date of its v0.2.1 captured assets");
}

const sitemap = readFileSync(join(docs, "sitemap.xml"), "utf8");
for (const page of ["https://hkjang.github.io/moyro/", "https://hkjang.github.io/moyro/screens.html"]) {
  if (!sitemap.includes(page)) failures.push(`sitemap is missing canonical URL: ${page}`);
}
const sitemapDates = [...sitemap.matchAll(/<lastmod>([^<]+)<\/lastmod>/g)].map((match) => match[1]);
if (sitemapDates.filter((date) => date === "2026-09-04").length !== 4
  || sitemapDates.filter((date) => date === "2026-08-30").length !== 1) {
  failures.push("sitemap must date current v0.2.15 pages at 2026-09-04 and the v0.2.1 gallery at 2026-08-30");
}

try {
  const manifest = JSON.parse(readFileSync(join(docs, "site.webmanifest"), "utf8"));
  if (manifest.short_name !== "moyro" || manifest.theme_color !== "#101A3B") {
    failures.push("site.webmanifest is missing the canonical moyro identity/theme");
  }
  for (const icon of manifest.icons ?? []) {
    if (!existsSync(join(docs, icon.src))) failures.push(`site.webmanifest references missing icon: ${icon.src}`);
  }
  if (!(manifest.icons ?? []).some((icon) => String(icon.purpose).includes("maskable"))) {
    failures.push("site.webmanifest is missing a maskable app icon");
  }
} catch (error) {
  failures.push(`invalid site.webmanifest: ${error.message}`);
}

if (failures.length) {
  console.error(failures.map((item) => `- ${item}`).join("\n"));
  process.exit(1);
}
console.log(
  `Pages verification passed: ${htmlFiles.length} HTML pages, ` +
  `${expectedScreenshots.length} product screenshots, ${navigationRoutes.size} navigable settings/admin routes, ` +
  `${expectedBrandPNGs.size} brand PNGs.`,
);

function walk(directory) {
  const result = [];
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) result.push(...walk(path));
    else if (entry.isFile() && statSync(path).size >= 0) result.push(path);
  }
  return result;
}

function readJPEGDimensions(bytes) {
  if (bytes.length < 4 || bytes[0] !== 0xff || bytes[1] !== 0xd8) {
    throw new Error("missing SOI marker");
  }
  const startOfFrameMarkers = new Set([
    0xc0, 0xc1, 0xc2, 0xc3, 0xc5, 0xc6, 0xc7,
    0xc9, 0xca, 0xcb, 0xcd, 0xce, 0xcf,
  ]);
  let offset = 2;
  while (offset < bytes.length) {
    if (bytes[offset] !== 0xff) {
      offset += 1;
      continue;
    }
    while (offset < bytes.length && bytes[offset] === 0xff) offset += 1;
    if (offset >= bytes.length) break;
    const marker = bytes[offset];
    offset += 1;

    if (marker === 0xd9 || marker === 0xda) break;
    if (marker === 0x01 || (marker >= 0xd0 && marker <= 0xd8)) continue;
    if (offset + 2 > bytes.length) throw new Error("truncated segment length");
    const segmentLength = bytes.readUInt16BE(offset);
    if (segmentLength < 2 || offset + segmentLength > bytes.length) {
      throw new Error("invalid segment length");
    }
    if (startOfFrameMarkers.has(marker)) {
      if (segmentLength < 7) throw new Error("truncated SOF segment");
      return {
        width: bytes.readUInt16BE(offset + 5),
        height: bytes.readUInt16BE(offset + 3),
      };
    }
    offset += segmentLength;
  }
  throw new Error("SOF marker not found");
}

function readPNGDimensions(bytes) {
  const signature = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
  if (bytes.length < 24 || !bytes.subarray(0, 8).equals(signature) || bytes.toString("ascii", 12, 16) !== "IHDR") {
    throw new Error("missing PNG signature or IHDR chunk");
  }
  return { width: bytes.readUInt32BE(16), height: bytes.readUInt32BE(20) };
}
