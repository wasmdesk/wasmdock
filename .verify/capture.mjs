import { chromium } from "playwright";

const URL = process.env.HARNESS_URL || "http://localhost:8099/.verify/harness.html";

const browser = await chromium.launch({ channel: "chrome", headless: true });
const page = await browser.newPage({ viewport: { width: 480, height: 120 } });
const logs = [];
page.on("console", (m) => logs.push(m.text()));
page.on("pageerror", (e) => logs.push("PAGEERROR: " + e.message));

await page.goto(URL, { waitUntil: "load" });
await page.waitForFunction(() => window.__done === true || window.__error, null, { timeout: 8000 });

const err = await page.evaluate(() => window.__error || null);
if (err) { console.log("WASM ERROR:", err); console.log(logs.join("\n")); await browser.close(); process.exit(1); }

const state = await page.evaluate(() => ({
  committed: window.__committed | 0,
  blitted: !!window.__blitted,
  inputReady: !!window.__inputReady,
  launches: window.__launches || [],
}));

// Screenshot the canvas element.
const el = await page.$("#c");
await el.screenshot({ path: "shot.png" });

// Pixel analysis: read the canvas back as raw RGBA via the page.
const stats = await page.evaluate(() => {
  const c = document.getElementById("c");
  const ctx = c.getContext("2d");
  const { data, width, height } = ctx.getImageData(0, 0, c.width, c.height);
  let nonBg = 0, total = width * height;
  // Desktop backdrop is #3a5a8c. Count pixels that differ from it (= dock paint).
  const bg = [0x3a, 0x5a, 0x8c];
  // Row coverage: which rows contain dock paint (to confirm a bottom bar).
  const rowHit = new Array(height).fill(0);
  // Column coverage in the bar band (to confirm a centered horizontal bar).
  const colHit = new Array(width).fill(0);
  for (let y = 0; y < height; y++) {
    for (let x = 0; x < width; x++) {
      const i = (y * width + x) * 4;
      const dr = Math.abs(data[i] - bg[0]);
      const dg = Math.abs(data[i + 1] - bg[1]);
      const db = Math.abs(data[i + 2] - bg[2]);
      if (dr + dg + db > 24) { nonBg++; rowHit[y]++; colHit[x]++; }
    }
  }
  // Topmost / bottommost rows with paint.
  let firstRow = -1, lastRow = -1;
  for (let y = 0; y < height; y++) if (rowHit[y] > 0) { if (firstRow < 0) firstRow = y; lastRow = y; }
  let firstCol = -1, lastCol = -1;
  for (let x = 0; x < width; x++) if (colHit[x] > 0) { if (firstCol < 0) firstCol = x; lastCol = x; }
  // Corner alpha sample from the live surface buffer.
  return {
    width, height, total, nonBg,
    nonBgPct: +(100 * nonBg / total).toFixed(1),
    firstPaintRow: firstRow, lastPaintRow: lastRow,
    firstPaintCol: firstCol, lastPaintCol: lastCol,
    leftMargin: firstCol, rightMargin: width - 1 - lastCol,
  };
});

console.log("=== render state ===");
console.log(JSON.stringify(state, null, 2));
console.log("=== pixel stats ===");
console.log(JSON.stringify(stats, null, 2));
console.log("=== browser console ===");
console.log(logs.join("\n"));

// Assertions.
const fails = [];
if (stats.nonBg === 0) fails.push("blank canvas: no dock pixels");
if (stats.nonBgPct > 95) fails.push("canvas almost entirely painted (not a panel)");
// Bottom-anchored: paint should reach the lower portion of the surface.
if (stats.lastPaintRow < stats.height * 0.6) fails.push("no paint in bottom band");
// Centered: left/right margins roughly equal (bar is centered).
if (Math.abs(stats.leftMargin - stats.rightMargin) > 24) fails.push("bar not horizontally centered");
if (!state.launches.includes("editor") && state.launches.length === 0) fails.push("click produced no launch message");

console.log("=== result ===");
if (fails.length) { console.log("FAIL:\n - " + fails.join("\n - ")); await browser.close(); process.exit(2); }
console.log("PASS: non-blank, bottom-anchored, centered dock; launch fired:", JSON.stringify(state.launches));
await browser.close();
