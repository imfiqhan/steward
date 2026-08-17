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
    // An open menu is moved to <body>, so it is found by its id rather than
    // inside the wrapper it belongs to.
    const pop = document.getElementById(wrap.id + "-popover");
    const t = wrap.querySelector("button").getBoundingClientRect();
    const p = pop.getBoundingClientRect();
    const at = (y) => {
      const el = document.elementFromPoint(p.left + p.width / 2, y);
      return !!el && (pop.contains(el) || pop === el);
    };
    return {
      gap: p.top >= t.bottom ? p.top - t.bottom : t.top - p.bottom,
      edge: Math.abs(p.right - t.right),
      inView: p.top >= 0 && p.bottom <= window.innerHeight &&
        p.left >= 0 && p.right <= window.innerWidth,
      width: p.width,
      onTop: at(p.top + 8),
      // The last item is the half that gets lost: a menu positioned inside the
      // scrolling table rather than against the viewport is clipped at the
      // container's bottom edge, which is exactly where the pager begins.
      lastItemVisible: at(p.bottom - 8),
    };
  });
  if (!m) { check(false, `row ${idx} menu opens`); continue; }
  check(Math.abs(m.gap) < 12, `row ${idx} menu sits against its trigger (${m.gap.toFixed(1)}px)`);
  check(m.edge < 40, `row ${idx} menu aligns to the trigger's edge`);
  check(m.inView, `row ${idx} menu stays inside the viewport`);
  check(m.width > 100 && m.width < 400, `row ${idx} menu is not stretched (${m.width.toFixed(0)}px)`);
  check(m.onTop, `row ${idx} menu paints above the rows below it`);
  check(m.lastItemVisible, `row ${idx} menu's last item is not clipped by the pager`);
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
    tabbable: [...document.getElementById(wrap.id + "-popover")
      .querySelectorAll('[role="menuitem"]')]
      .every((i) => i.getAttribute("tabindex") === "-1"),
  };
});
// The move is the whole point: nothing inside the table can clip a child of
// body, whatever an engine decides a fixed element's containing block is.
const portal = await page.evaluate(() => {
  const pop = document.querySelector("[data-popover][data-steward-portal]");
  if (!pop) return { portaled: false, clippers: -1 };
  let clippers = 0;
  for (let el = pop.parentElement; el; el = el.parentElement) {
    if (getComputedStyle(el).overflow !== "visible") clippers++;
  }
  return { portaled: pop.closest("#steward-menu-portal") !== null, clippers };
});
check(portal.portaled, "an open menu is moved out of the table");
// Moved out, the menu no longer matches a rule written for its wrapper's
// child: it falls back to absolute, and animates left and top along with the
// fade, which reads as the menu sliding in from somewhere else.
const settle = await page.evaluate(() => {
  const pop = document.querySelector("[data-popover][data-steward-portal]");
  const cs = getComputedStyle(pop);
  return {
    position: cs.position,
    animates: /(^|[ ,])(left|top|all)([ ,]|$)/.test(cs.transitionProperty),
  };
});
check(settle.position === "fixed", `the moved menu is still fixed (${settle.position})`);
check(!settle.animates, "it does not animate its way in from elsewhere");
check(portal.clippers === 0, `nothing above it can clip it (${portal.clippers})`);

check(kb.onTrigger, "opening a menu leaves focus on its trigger");
check(!!kb.active, `arrow keys track the highlighted item by id (${kb.active})`);
check(kb.tabbable, "menu items stay out of the tab order");
await page.keyboard.press("Enter");
await page.waitForTimeout(700);
check(/\/authors\/\d/.test(page.url()), `Enter activates the highlighted item (${page.url()})`);

// --- composed page (steward.Row / steward.Col) --------------------------------
// A column is a grid item, and a grid item's default min-width lets its content
// push it past the track it was given: a chart's canvas did exactly that, so a
// card in a four-column slot measured 528px against a 355px column. Widths are
// the check, plus that the page itself never scrolls sideways.
await page.goto(BASE + "/posts/report");
await page.waitForTimeout(900);
const composed = await page.evaluate(() => {
  const rows = [...document.querySelectorAll(".steward-layout-row")];
  const cols = rows.flatMap((r) => [...r.children].map((c) => {
    const cr = c.getBoundingClientRect();
    const card = c.querySelector(".card");
    const kr = card ? card.getBoundingClientRect() : null;
    return {
      span: (c.className.match(/steward-span-(\d+)/) || [])[1],
      width: Math.round(cr.width),
      overflow: kr ? Math.round(kr.right - cr.right) : 0,
    };
  }));
  return {
    rows: rows.length,
    cols,
    wide: cols.length ? Math.max(...cols.map((c) => c.width)) : 0,
    narrow: cols.length ? Math.min(...cols.map((c) => c.width)) : 0,
    sideways: document.documentElement.scrollWidth > window.innerWidth,
    hasTable: !!document.querySelector(".steward-layout-row table"),
    hasMetric: /Published/.test(document.body.textContent),
  };
});
check(composed.rows === 2, `the page renders its rows (${composed.rows})`);
check(composed.cols.every((c) => c.overflow <= 1),
  `every card stays inside its column (${composed.cols.map((c) => c.overflow).join(",")})`);
check(composed.wide > composed.narrow,
  `an eight-column slot is wider than a four (${composed.wide} vs ${composed.narrow})`);
check(!composed.sideways, "a composed page does not scroll sideways");
check(composed.hasTable && composed.hasMetric, "the table and metric widgets render");

// Below the breakpoint a column takes the whole row: two columns on a phone are
// two columns too narrow to read.
await page.setViewportSize({ width: 480, height: 800 });
await page.waitForTimeout(300);
const stacked = await page.evaluate(() => {
  const cols = [...document.querySelectorAll(".steward-layout-row")[0].children];
  const widths = cols.map((c) => Math.round(c.getBoundingClientRect().width));
  return { widths, same: new Set(widths).size === 1 };
});
check(stacked.same, `on a narrow screen the columns stack (${stacked.widths.join(",")})`);
await page.setViewportSize({ width: 1400, height: 900 });

// --- preview viewer -----------------------------------------------------------
// A thumbnail is too small to read and a stored file is only a path, so both
// open in place. What matters is that the trigger carries no button chrome, the
// dialog actually shows the file, and a click beside it closes.
await page.goto(BASE + "/posts");
await page.waitForTimeout(300);
const thumbs = await page.locator("[data-steward-preview]").count();
if (thumbs > 0) {
  const chrome = await page.evaluate(() => {
    const t = document.querySelector(".steward-preview-trigger");
    const cs = getComputedStyle(t);
    return { border: cs.borderTopWidth, pad: cs.paddingTop, cursor: cs.cursor };
  });
  check(chrome.border === "0px" && chrome.pad === "0px",
    `a thumbnail trigger looks like the image, not a button (${chrome.border}/${chrome.pad})`);
  check(chrome.cursor === "zoom-in", `the trigger says it can be opened (${chrome.cursor})`);

  await page.locator("[data-steward-preview]").first().click();
  await page.waitForTimeout(350);
  const shown = await page.evaluate(() => {
    const d = document.getElementById("steward-preview");
    const img = d.querySelector("img");
    return {
      open: d.open,
      hasImage: !!img,
      fits: img ? img.getBoundingClientRect().height <= window.innerHeight : true,
      url: window.location.pathname,
    };
  });
  const placed = await page.evaluate(() => {
    const d = document.getElementById("steward-preview");
    const r = d.getBoundingClientRect();
    return {
      dx: Math.round(r.left - (window.innerWidth - r.width) / 2),
      dy: Math.round(r.top - (window.innerHeight - r.height) / 2),
    };
  });
  // A modal dialog centres through the user agent's margin:auto, which the
  // stylesheet reset replaces with 0 — leaving it in the top-left corner.
  check(Math.abs(placed.dx) <= 2 && Math.abs(placed.dy) <= 2,
    `the viewer is centred (off by ${placed.dx},${placed.dy})`);
  check(shown.open, "clicking a thumbnail opens the viewer");
  check(shown.hasImage, "the viewer shows the picture");
  check(shown.fits, "the picture is scaled to the screen");
  check(/\/posts$/.test(shown.url), "opening a picture does not navigate away from the list");

  await page.mouse.click(8, 8);
  await page.waitForTimeout(250);
  check(await page.evaluate(() => !document.getElementById("steward-preview").open),
    "clicking beside the viewer closes it");
} else {
  console.log("  -- no previewable media on /posts, viewer not exercised");
}

// --- multi-select combobox ----------------------------------------------------
// The widget's whole point is that it filters and holds several values, and its
// hidden input is what the server receives. None of that is visible in markup.
await page.goto(BASE + "/posts/1/edit");
const hasCombo = await page.locator(".combobox [role=listbox][aria-multiselectable=true]").count();
if (hasCombo) {
  // Scoped to the multiple one: the same form now renders single-select
  // comboboxes too, and they store a plain value rather than an array.
  const box = page.locator(".combobox:has([role=listbox][aria-multiselectable=true])").first();
  const input = box.locator("input[role=combobox]");
  const hidden = box.locator("input[type=hidden]");

  const before = await hidden.inputValue();
  check(before.trim().startsWith("["), `the hidden input holds a JSON array (${before})`);

  await input.click();
  await page.waitForTimeout(200);
  const opened = await box.locator("[data-popover]").getAttribute("aria-hidden");
  check(opened === "false", "clicking the input opens the list");

  const total = await box.locator("[role=option]").count();
  check(total > 0 && total <= 50, `the page ships one page of options (${total})`);

  // The popover is width:max-content, so one long label used to stretch the
  // whole list past the field and out of the form. Options carry truncate, but
  // truncation needs a width to truncate against.
  const w = await page.evaluate(() => {
    const root = document.querySelector(".combobox:has([aria-multiselectable=true])");
    const pop = root.querySelector("[data-popover]");
    return {
      over: Math.round(pop.getBoundingClientRect().right - root.getBoundingClientRect().right),
      titled: [...pop.querySelectorAll('[role="option"]')].every((o) => !!o.title),
    };
  });
  check(w.over <= 1, `the list stays inside its field (${w.over}px past it)`);
  check(w.titled, "a truncated option is still readable in full on hover");

  // The list is fixed and placed from script, so where it lands is computed
  // layout. Absolutely positioned it was clipped by the form card and by the
  // shell's scroll container, which cut a field low on a form mid-item.
  const place = await page.evaluate(() => {
    const root = document.querySelector(".combobox:has([aria-multiselectable=true])");
    const pop = root.querySelector("[data-popover]");
    const field = root.querySelector(".input-group") || root;
    const p = pop.getBoundingClientRect();
    const f = field.getBoundingClientRect();
    const hit = document.elementFromPoint(p.left + p.width / 2, p.bottom - 4);
    return {
      fixed: getComputedStyle(pop).position === "fixed",
      dx: Math.round(p.left - f.left),
      dw: Math.round(p.width - f.width),
      inView: p.top >= 0 && p.bottom <= window.innerHeight,
      lastRowPainted: pop.contains(hit),
    };
  });
  check(place.fixed, "the list escapes its ancestors' clipping");
  check(Math.abs(place.dx) <= 2, `the list aligns to its field (${place.dx}px off)`);
  check(Math.abs(place.dw) <= 2, `the list is the width of its field (${place.dw}px off)`);
  check(place.inView, "the list stays inside the viewport");
  check(place.lastRowPainted, "the list's last row is on screen, not clipped");

  await page.keyboard.press("Escape");
  await page.waitForTimeout(150);

  // A field close to the bottom edge: the list opens upwards, and when it fits
  // neither way it takes the larger gap and scrolls within it.
  await page.setViewportSize({ width: 1400, height: 620 });
  await page.evaluate(() => {
    const sc = document.querySelector(".overflow-y-auto");
    const root = document.querySelector(".combobox:has([aria-multiselectable=true])");
    const field = root && (root.querySelector(".input-group") || root);
    if (!sc || !field) return;
    for (let i = 0; i < 300; i++) {
      if (field.getBoundingClientRect().bottom > window.innerHeight - 200) break;
      sc.scrollTop += 10;
    }
  });
  await page.waitForTimeout(200);
  await input.click();
  await page.waitForTimeout(250);
  const low = await page.evaluate(() => {
    const root = document.querySelector(".combobox:has([aria-multiselectable=true])");
    const pop = root.querySelector("[data-popover]");
    const p = pop.getBoundingClientRect();
    const f = (root.querySelector(".input-group") || root).getBoundingClientRect();
    const hit = document.elementFromPoint(p.left + p.width / 2, p.bottom - 4);
    return {
      above: p.bottom <= f.top + 2,
      inView: p.top >= 0 && p.bottom <= window.innerHeight,
      lastRowPainted: pop.contains(hit),
      height: Math.round(p.height),
    };
  });
  check(low.above, "a field near the bottom opens its list upwards");
  check(low.inView, `the flipped list stays inside the viewport (${low.height}px tall)`);
  check(low.lastRowPainted, "the flipped list's last row is on screen");
  await page.setViewportSize({ width: 1400, height: 900 });
  await page.keyboard.press("Escape");
  await page.waitForTimeout(150);

  // Filtering is the server's now, so this is a round trip rather than a
  // client-side hide: the list should come back different.
  const label = await box.locator("[role=option]").first().getAttribute("data-label");
  await input.fill(label.slice(0, 3));
  await page.waitForTimeout(600);
  const visible = await box.locator("[role=option]:visible").count();
  check(visible > 0 && visible <= total, `typing fetches a narrower list (${visible}/${total})`);
  const stillMatches = await box.locator("[role=option]:visible").first().getAttribute("data-label");
  check(stillMatches.toLowerCase().includes(label.slice(0, 3).toLowerCase()),
    `what came back matches the query (${stillMatches})`);

  // The query has to survive the fetch. refresh() re-applies the stored
  // selection, and for a multiple combobox that empties the input — right after
  // picking a chip, wrong while the reader is still typing. It wiped what they
  // had typed about 200ms after they stopped.
  await input.fill("");
  await page.waitForTimeout(400);
  for (const ch of label.slice(0, 3)) {
    await input.press(ch);
    await page.waitForTimeout(120);
  }
  await page.waitForTimeout(600);
  check((await input.inputValue()) === label.slice(0, 3),
    `the typed query survives the fetch (${JSON.stringify(await input.inputValue())})`);

  // A query with no matches must not leave the previous list on screen.
  await input.fill("zzzzqqq");
  await page.waitForTimeout(600);
  const none = await box.locator("[role=option]:visible").count();
  check(none === 0, `a query matching nothing empties the list (${none} left)`);
  await input.fill(label.slice(0, 3));
  await page.waitForTimeout(600);

  await box.locator("[role=option]:visible").first().click();
  await page.waitForTimeout(200);
  const after = await hidden.inputValue();
  let parsed = [];
  try { parsed = JSON.parse(after); } catch { /* reported below */ }
  check(Array.isArray(parsed), `selecting writes JSON to the hidden input (${after})`);
  check(JSON.stringify(parsed) !== before, "the selection actually changed the submitted value");

  // Chips are how a multiple combobox shows what is held. The component builds
  // them by moving the input into its own wrapper, and gives up silently when
  // the input is nested in anything — so a selection that exists in the hidden
  // input but has no chip means the control is rendering a selection nobody can
  // see. Asserted on the chips alone: an earlier version of this check also
  // accepted the hidden input, and passed while every chip was missing.
  const chips = await box.locator(".combobox-chip").count();
  check(chips === parsed.length,
    `every selected value has a chip (${chips} chips, ${parsed.length} selected)`);
  const inputIsDirectChild = await box.evaluate((root) =>
    root.querySelector('input[role="combobox"]').closest(".combobox-chips, .combobox") === root ||
    root.querySelector('input[role="combobox"]').parentElement.classList.contains("combobox-chips"));
  check(inputIsDirectChild, "the input sits where the chip surface can take it");
} else {
  check(false, "the posts form renders a multi-select combobox");
}

// --- single-select combobox ---------------------------------------------------
// Select and BelongsTo render the same control, storing the value the <select>
// they replaced would have submitted.
{
  const box = page.locator(".combobox:not(:has([aria-multiselectable=true]))").first();
  if (await box.count()) {
    const input = box.locator("input[role=combobox]");
    const hidden = box.locator("input[type=hidden]");
    const before = await hidden.inputValue();
    check(!before.trim().startsWith("["),
      `a single select stores a bare value, not an array (${JSON.stringify(before)})`);
    check((await input.inputValue()).length > 0,
      `the control shows the current selection's label (${JSON.stringify(await input.inputValue())})`);

    await input.click();
    await page.waitForTimeout(300);
    const opts = box.locator("[role=option]:visible");
    const n = await opts.count();
    if (n > 1) {
      const wanted = await opts.nth(1).getAttribute("data-value");
      await opts.nth(1).click();
      await page.waitForTimeout(250);
      check((await hidden.inputValue()) === wanted,
        `picking one writes its value (${await hidden.inputValue()} === ${wanted})`);
    } else {
      check(false, `the single select offered ${n} options to choose between`);
    }
  } else {
    check(false, "the form renders a single-select combobox");
  }
}

// --- upload field --------------------------------------------------------------
// The native control draws itself and cannot show a value it did not receive,
// so an edit form used to report "no file selected" over a record that had one.
await page.goto(BASE + "/posts/1/edit");
const up = page.locator("[data-steward-upload]").first();
if (await up.count()) {
  const empty = await up.evaluate((r) => ({
    native: !!r.querySelector('input[type=file]:not(.sr-only)'),
    prompt: !r.querySelector("[data-steward-upload-pick]").hidden,
    text: r.textContent.replace(/\s+/g, " ").trim(),
  }));
  check(!empty.native, "the native file control is not what you see");
  check(/up to \d+ ?(MB|KB)/.test(empty.text), `the field states its size limit (${empty.text})`);

  await page.setInputFiles("[data-steward-upload] input[type=file]", {
    name: "Annual Report 2026.png",
    mimeType: "image/png",
    buffer: Buffer.from(
      "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8DwHwAFAAH/q842iQAAAABJRU5ErkJggg==",
      "base64"),
  });
  await page.waitForTimeout(1200);
  const filled = await up.evaluate((r) => ({
    value: r.querySelector("[data-steward-upload-value]").value,
    name: r.querySelector("[data-steward-upload-link]").textContent.trim(),
    prompt: !r.querySelector("[data-steward-upload-pick]").hidden,
    remove: !!r.querySelector("[data-steward-upload-remove]"),
  }));
  check(!!filled.value, `uploading sets the submitted value (${filled.value})`);
  check(!filled.prompt, "the prompt gives way to what is held");
  check(/Annual.Report.2026/.test(filled.name),
    `the name survives instead of a token (${filled.name})`);
  check(filled.remove, "an uploaded file can be removed");

  await up.locator("[data-steward-upload-remove]").click();
  await page.waitForTimeout(200);
  const cleared = await up.evaluate((r) => ({
    value: r.querySelector("[data-steward-upload-value]").value,
    prompt: !r.querySelector("[data-steward-upload-pick]").hidden,
  }));
  check(cleared.value === "" && cleared.prompt, "removing clears the value");
} else {
  check(false, "the posts form renders an upload field");
}

// --- several files in one field ------------------------------------------------
// The list in the DOM and the JSON array in the column have to stay the same
// thing; only a browser can add three files and check what the column ends up
// holding.
{
  const multi = page.locator('[data-steward-field="Attachments"] .steward-upload');
  if (await multi.count()) {
    const mk = (name) => ({ name, mimeType: "text/plain", buffer: Buffer.from(name) });
    await page.setInputFiles('[data-steward-field="Attachments"] input[type=file]',
      [mk("One.txt"), mk("Two.txt")]);
    await page.waitForTimeout(2000);
    const st = await multi.evaluate((r) => ({
      rows: r.querySelectorAll("[data-steward-upload-item]").length,
      value: r.querySelector("[data-steward-upload-value]").value,
    }));
    let arr = [];
    try { arr = JSON.parse(st.value); } catch { /* checked below */ }
    check(st.rows === 2, `two files land as two rows (${st.rows})`);
    check(Array.isArray(arr) && arr.length === 2,
      `the column holds a JSON array (${st.value.slice(0, 80)})`);

    await multi.locator("[data-steward-upload-remove]").first().click();
    await page.waitForTimeout(300);
    const after = await multi.evaluate((r) => ({
      rows: r.querySelectorAll("[data-steward-upload-item]").length,
      len: JSON.parse(r.querySelector("[data-steward-upload-value]").value || "[]").length,
    }));
    check(after.rows === 1 && after.len === 1, "removing a row drops it from the array");
  } else {
    check(false, "the posts form renders a multi-file field");
  }
}

// --- date picker ---------------------------------------------------------------
// The native input stays the field; a calendar is added only where there is a
// pointer. Which half runs is a media query, so it can only be seen in a
// browser.
{
  const dp = page.locator("[data-steward-datepicker]").first();
  if (await dp.count()) {
    check(await dp.locator(".steward-datepicker-trigger").count() === 1,
      "a calendar trigger is added on a pointer device");
    await dp.locator(".steward-datepicker-trigger").click();
    await page.waitForTimeout(300);
    const cal = await dp.evaluate((r) => {
      const c = r.querySelector(".steward-cal");
      if (!c) return null;
      const box = c.getBoundingClientRect();
      return {
        days: c.querySelectorAll(".steward-cal-day").length,
        inView: box.bottom <= window.innerHeight + 1 && box.top >= -1,
        selected: c.querySelector('[aria-selected="true"]')?.dataset.iso || null,
      };
    });
    check(cal !== null, "the calendar opens");
    if (cal) {
      check(cal.days === 42, `the grid is six weeks (${cal.days})`);
      check(cal.inView, "the calendar stays on screen");
      // Stepping the month replaces the calendar's children, so the button
      // just pressed is detached by the time the click reaches the document —
      // which read as a click outside and closed what was being navigated.
      const first = await dp.locator(".steward-cal-title").textContent();
      await dp.locator(".steward-cal-nav").nth(1).click();
      await page.waitForTimeout(200);
      const stillOpen = await dp.locator(".steward-cal").count();
      check(stillOpen === 1, "stepping the month does not close the calendar");
      if (stillOpen) {
        const second = await dp.locator(".steward-cal-title").textContent();
        check(second !== first, `the month moved (${first} -> ${second})`);
      }

      // The heading zooms out: stepping a month at a time is hopeless for a
      // date three years back.
      await dp.locator(".steward-cal-title").click();
      await page.waitForTimeout(200);
      const months = await dp.locator(".steward-cal-cell").count();
      check(months === 12, `the heading opens a month grid (${months})`);
      await dp.locator(".steward-cal-title").click();
      await page.waitForTimeout(200);
      const years = await dp.locator(".steward-cal-cell").count();
      const span = await dp.locator(".steward-cal-title").textContent();
      check(years === 12, `and again for years (${years}, ${span})`);
      // Min and Max reach the calendar, not just the control.
      const barred = await dp.locator(".steward-cal-cell:disabled").count();
      check(barred > 0, `years outside the range are not selectable (${barred})`);
      await dp.locator(".steward-cal-cell:not(:disabled)").first().click();
      await page.waitForTimeout(200);
      await dp.locator(".steward-cal-cell:not(:disabled)").first().click();
      await page.waitForTimeout(200);
      check(await dp.locator(".steward-cal-day").count() === 42,
        "picking a year then a month lands back on days");

      const before = await dp.locator("input[name]").inputValue();
      await dp.locator(".steward-cal-day:not([data-outside])").first().click();
      await page.waitForTimeout(200);
      const after = await dp.locator("input[name]").inputValue();
      check(after !== before || !before, `picking a day writes the input (${after})`);
      check(await dp.locator(".steward-cal").count() === 0, "picking closes the calendar");
    }
  } else {
    check(false, "the form renders a date picker");
  }
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
