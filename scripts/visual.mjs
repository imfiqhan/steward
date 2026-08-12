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
  const nav = toggle.closest("header > div");
  const t = toggle.getBoundingClientRect();
  const n = nav.getBoundingClientRect();
  return { t: t.toJSON(), n: n.toJSON(), vw: window.innerWidth };
});
check(header.n.right > header.vw * 0.7, "header user block sits on the right");
check(header.t.height > 16 && header.t.height < 80, "theme toggle has sane height");

// --- grid: action buttons not collapsed ------------------------------------
await page.goto(BASE + "/posts");
await page.waitForSelector("td .inline-flex");
const rows = await page.evaluate(() =>
  [...document.querySelectorAll("td .inline-flex")].slice(0, 5).map(list =>
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
  const toastText = await page.locator("#toaster").textContent();
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
  const toastText = await page.locator("#toaster").textContent();
  check(/published/i.test(toastText || ""), "row action runs and toasts");
} else {
  check(false, "row action button present");
}

// --- one scrollbar per page, and none of it spare -----------------------------
// The height chain hands the table's container whatever the card has left, so
// the rows scroll there and nowhere else. An absolutely positioned box also pads
// its scroll container's scrollable overflow even while invisible, which a
// closed row menu below the last row did — leaving the table scrollable past its
// own end. Both are computed layout, so they can only be checked in an engine.
await page.goto(BASE + "/posts");
await page.waitForSelector(".table-container table");
const scroll = await page.evaluate(() => {
  const cont = document.querySelector(".table-container");
  const table = cont.querySelector("table");
  const pane = document.getElementById("page-content");
  return {
    excess: cont.scrollHeight - Math.ceil(table.getBoundingClientRect().height),
    fits: cont.scrollHeight <= cont.clientHeight,
    tableH: Math.ceil(table.getBoundingClientRect().height),
    contClient: cont.clientHeight,
    windowOver: document.documentElement.scrollHeight - window.innerHeight,
    paneOver: pane.scrollHeight - pane.clientHeight,
    headerSticks: getComputedStyle(table.querySelector("thead th")).position === "sticky",
  };
});
check(scroll.excess <= 1,
  `container scrolls no further than its table (excess ${scroll.excess}px)`);
check(scroll.windowOver <= 0, `the window itself does not scroll (${scroll.windowOver}px)`);
check(scroll.paneOver <= 0, `the content pane does not scroll on a grid (${scroll.paneOver}px)`);
check(scroll.headerSticks, "the header row is sticky");
// Whichever way this grid falls, it has to be consistent: rows that fit must
// leave nothing to scroll.
if (scroll.tableH <= scroll.contClient) {
  check(scroll.fits, "a table that fits its container leaves nothing to scroll");
} else {
  check(!scroll.fits, "a table taller than its container scrolls");
}

// --- row action menus: where they land, and the keyboard path -----------------
// The popover is fixed and positioned from script, so its placement, its width
// and what paints over it are all computed layout. The keyboard path is checked
// because the component keeps focus on the trigger and tracks the highlighted
// item by id — activation goes through that, not through the item's own focus.
await page.goto(BASE + "/authors");
await page.waitForSelector("[data-steward-menu] > button");
const trigs = await page.locator("[data-steward-menu] > button").all();
check(trigs.length > 0, "the menu-style grid renders row menus");
for (const idx of [...new Set([0, trigs.length - 1])]) {
  await trigs[idx].click();
  await page.waitForTimeout(250);
  const m = await page.evaluate(() => {
    const wrap = [...document.querySelectorAll("[data-steward-menu]")]
      .find((w) => w.querySelector("button").getAttribute("aria-expanded") === "true");
    if (!wrap) return null;
    const pop = wrap.querySelector("[data-popover]");
    const t = wrap.querySelector("button").getBoundingClientRect();
    const p = pop.getBoundingClientRect();
    const hit = document.elementFromPoint(p.left + p.width / 2, p.top + 8);
    return {
      gap: p.top >= t.bottom ? p.top - t.bottom : t.top - p.bottom,
      edge: Math.abs(p.right - t.right),
      inView: p.top >= 0 && p.bottom <= window.innerHeight &&
        p.left >= 0 && p.right <= window.innerWidth,
      width: p.width,
      onTop: pop.contains(hit) || pop === hit,
    };
  });
  if (!m) { check(false, `row ${idx} menu opens`); continue; }
  check(Math.abs(m.gap) < 12, `row ${idx} menu sits against its trigger (${m.gap.toFixed(1)}px)`);
  check(m.edge < 40, `row ${idx} menu aligns to the trigger's edge`);
  check(m.inView, `row ${idx} menu stays inside the viewport`);
  check(m.width > 100 && m.width < 400, `row ${idx} menu is not stretched (${m.width.toFixed(0)}px)`);
  check(m.onTop, `row ${idx} menu paints above the rows below it`);
  await page.keyboard.press("Escape");
  await page.waitForTimeout(150);
}

await trigs[0].click();
await page.waitForTimeout(200);
await page.keyboard.press("ArrowDown");
const kb = await page.evaluate(() => {
  const wrap = [...document.querySelectorAll("[data-steward-menu]")]
    .find((w) => w.querySelector("button").getAttribute("aria-expanded") === "true");
  const trig = wrap.querySelector("button");
  return {
    active: trig.getAttribute("aria-activedescendant"),
    onTrigger: document.activeElement === trig,
    tabbable: [...wrap.querySelectorAll('[role="menuitem"]')]
      .every((i) => i.getAttribute("tabindex") === "-1"),
  };
});
check(kb.onTrigger, "opening a menu leaves focus on its trigger");
check(!!kb.active, `arrow keys track the highlighted item by id (${kb.active})`);
check(kb.tabbable, "menu items stay out of the tab order");
await page.keyboard.press("Enter");
await page.waitForTimeout(700);
check(/\/authors\/\d/.test(page.url()), `Enter activates the highlighted item (${page.url()})`);

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
