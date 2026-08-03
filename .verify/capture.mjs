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
  popupCommits: window.__popupCommits | 0,
  blitted: !!window.__blitted,
  inputReady: !!window.__inputReady,
  launches: window.__launches || [],
  focuses: window.__focuses || [],
  closes: window.__closes || [],
  popups: (window.__popups || []).map((p) => ({ w: p.w, h: p.h, rel_x: p.rel_x, rel_y: p.rel_y })),
  geomRest: window.__geomRest || {},
  geomHover: window.__geomHover || {},
}));

const el = await page.$("#c");
await el.screenshot({ path: "shot.png" });

// Pixel analysis: read the canvas back as raw RGBA.
const stats = await page.evaluate(() => {
  const c = document.getElementById("c");
  const ctx = c.getContext("2d");
  const { data, width, height } = ctx.getImageData(0, 0, c.width, c.height);
  let nonBg = 0;
  const bg = [0x3a, 0x5a, 0x8c];
  for (let i = 0; i < data.length; i += 4) {
    if (Math.abs(data[i] - bg[0]) + Math.abs(data[i + 1] - bg[1]) + Math.abs(data[i + 2] - bg[2]) > 24) nonBg++;
  }
  return { width, height, nonBg, nonBgPct: +(100 * nonBg / (width * height)).toFixed(1) };
});

console.log("=== render state ===");
console.log(JSON.stringify(state, null, 2));
console.log("=== pixel stats ===");
console.log(JSON.stringify(stats, null, 2));
console.log("=== browser console ===");
console.log(logs.join("\n"));

// Helper: width of launcher 0 from a geometry snapshot.
const launcher0W = (g) => (g && g.launchers && g.launchers[0] ? g.launchers[0].w : 0);

const fails = [];
if (stats.nonBg === 0) fails.push("blank canvas: no dock pixels");
if (!state.inputReady) fails.push("dock never registered an input handler");
if (state.launches.length === 0) fails.push("left-click produced no launch message");
// Magnification: the hovered launcher is wider than at rest.
const restW = launcher0W(state.geomRest);
const hovW = launcher0W(state.geomHover);
if (!(restW > 0 && hovW > restW)) fails.push(`launcher did not magnify on hover (rest=${restW}, hover=${hovW})`);
// Right-click opened a context-menu popup with a sane size.
if (state.popups.length === 0) fails.push("right-click opened no context-menu popup");
else if (!(state.popups[0].w >= 120 && state.popups[0].h > 0)) fails.push("popup size out of range");
if (state.popupCommits === 0) fails.push("popup menu never painted (no commit)");

console.log("=== result ===");
if (fails.length) { console.log("FAIL:\n - " + fails.join("\n - ")); await browser.close(); process.exit(2); }
console.log(`PASS: dock renders; magnify rest=${restW}->hover=${hovW}; launches=${JSON.stringify(state.launches)}; popup=${JSON.stringify(state.popups[0])}`);
await browser.close();
