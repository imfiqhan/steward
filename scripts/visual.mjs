// Visual/behavioral regression check in real WebKit (Playwright).
// Boots against a running example server (default http://localhost:8321)
// and fails loudly on layout collapse or broken inline interactions.
//
// Usage:  node scripts/visual.mjs [baseURL]
// Needs:  npm i playwright && npx playwright install webkit
import { webkit } from "playwright";

const BASE = (process.argv[2] || "http://localhost:8321") + "/admin";
const failures = [];
const check = (ok, msg) => {
  console.log((ok ? "  ok " : "FAIL ") + msg);
  if (!ok) failures.push(msg);
};

const browser = await webkit.launch();
const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });

await page.goto(BASE + "/auth/login");
await page.fill("#login-username", "admin");
await page.fill("#login-password", "admin");
await page.click("button[type=submit]");
await page.waitForURL("**/admin/**");

// --- header: theme toggle aligned and on the right -------------------------
const header = await page.evaluate(() => {
  const toggle = document.querySelector("[data-steward-theme-toggle]");
  const nav = toggle.closest(".navbar-nav");
  const t = toggle.getBoundingClientRect();
  const n = nav.getBoundingClientRect();
  return { t: t.toJSON(), n: n.toJSON(), vw: window.innerWidth };
});
check(header.n.right > header.vw * 0.7, "header user block sits on the right");
check(header.t.height > 16 && header.t.height < 80, "theme toggle has sane height");

// --- grid: action buttons not collapsed ------------------------------------
await page.goto(BASE + "/posts");
await page.waitForSelector("td .btn-list");
const rows = await page.evaluate(() =>
  [...document.querySelectorAll("td .btn-list")].slice(0, 5).map(list =>
    [...list.querySelectorAll(".btn")].map(b => {
      const r = b.getBoundingClientRect();
      return { x: r.x, w: r.width, h: r.height };
    })
  )
);
const btns = rows.flat();
check(btns.length > 0, "grid has action buttons");
check(btns.every(b => b.w >= 20 && b.h >= 20), "action buttons are at least 20px (not collapsed)");
let overlap = false;
for (const row of rows) {
  for (let i = 1; i < row.length; i++) {
    if (row[i].x < row[i - 1].x + row[i - 1].w - 1) overlap = true;
  }
}
check(!overlap, "action buttons do not overlap within a row");

// --- inline switch round-trip -----------------------------------------------
const sw = page.locator("[data-steward-inline-switch]").first();
if (await sw.count()) {
  const before = await sw.isChecked();
  await sw.click();
  await page.waitForTimeout(500);
  const after = await sw.isChecked();
  check(before !== after, "inline switch toggles");
  const toastText = await page.locator("#steward-toasts").textContent();
  check(/saved/i.test(toastText || ""), "inline switch shows saved toast");
  await sw.click(); // restore
  await page.waitForTimeout(300);
} else {
  check(false, "inline switch present on posts grid");
}

// --- inline editable round-trip ----------------------------------------------
const cell = page.locator("[data-steward-editable]").first();
if (await cell.count()) {
  const original = (await cell.textContent()).trim();
  await cell.click();
  const input = page.locator("[data-steward-editable] input");
  check(await input.count() === 1, "editable cell opens an input");
  await input.fill(original); // unchanged value = no request, cancels cleanly
  await input.press("Enter");
  await page.waitForTimeout(200);
  check((await cell.textContent()).trim() === original, "editable cell restores text");
} else {
  check(false, "editable cell present on posts grid");
}

// --- row action ---------------------------------------------------------------
const act = page.locator("[data-steward-action][data-ids]").first();
if (await act.count()) {
  await act.click();
  await page.waitForTimeout(500);
  const toastText = await page.locator("#steward-toasts").textContent();
  check(/published/i.test(toastText || ""), "row action runs and toasts");
} else {
  check(false, "row action button present");
}

await page.screenshot({
  path: "/tmp/steward-visual.png",
  fullPage: false,
});
await browser.close();

if (failures.length > 0) {
  console.error(`\n${failures.length} visual check(s) failed`);
  process.exit(1);
}
console.log("\nvisual OK");
