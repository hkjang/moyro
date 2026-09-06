# Render-and-look helper

Create `webapp/src/__preview.test.tsx` (delete afterwards):

```tsx
// @vitest-environment jsdom
import { renderToStaticMarkup } from "react-dom/server";
import { it } from "vitest";
import { writeFileSync } from "node:fs";
import { BuiltinSticker } from "@/features/workspace/stickers/Sticker";
import { STICKERS } from "@/features/workspace/stickers/stickers";

it("writes a preview page", () => {
  const cells = STICKERS.map((spec) =>
    `<div style="display:inline-block;margin:6px;text-align:center;font:12px sans-serif">${renderToStaticMarkup(<BuiltinSticker spec={spec} size={120} />)}<div>${spec.id}</div></div>`).join("");
  writeFileSync("/tmp/stickers.html", `<!doctype html><meta charset="utf-8"><body style="background:#f6f7f9;width:1160px">${cells}</body>`);
});
```

Then `npx vitest run src/__preview.test.tsx` and screenshot with the
bundled Chromium:

```js
const { chromium } = require("@playwright/test");
(async () => { const b = await chromium.launch(); const p = await b.newPage({ viewport: { width: 1200, height: 1400 } });
  await p.goto("file:///tmp/stickers.html"); await p.screenshot({ path: "/tmp/stickers.png", fullPage: true }); await b.close(); })();
```

View the PNG. Also render a 52px strip to confirm legibility at picker size.
