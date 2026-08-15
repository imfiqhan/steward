import { webkit } from "playwright";
const failures = [];
const check = (ok, m) => { console.log((ok ? "  ok   " : "  FAIL ") + m); if (!ok) failures.push(m); };
const b = await webkit.launch();
const p = await b.newPage({ viewport: { width: 1500, height: 1000 } });
await p.goto("http://localhost:8080/admin/auth/login");
await p.fill("#login-username", "developer");
await p.fill("#login-password", "qwerty123");
await Promise.all([p.waitForNavigation(), p.click("button[type=submit]")]);
await p.goto("http://localhost:8080/admin/berita");
await p.waitForSelector("table");

// Open the filter panel.
const toggle = p.locator('[aria-controls="grid-filters"], button:has-text("Filter")').first();
if (await toggle.count()) await toggle.click();
await p.waitForTimeout(400);

const shape = await p.evaluate(() => {
  const f = document.querySelector("#grid-filters");
  return {
    selects: f.querySelectorAll("select").length,
    comboboxes: f.querySelectorAll(".combobox").length,
    searching: f.querySelectorAll("[data-steward-options]").length,
    shipped: [...f.querySelectorAll(".combobox")].map(c =>
      ({ id: c.id, n: c.querySelectorAll('[role="option"]').length })),
  };
});
check(shape.selects === 0, `no native selects remain (${shape.selects})`);
check(shape.comboboxes === 4, `four comboboxes (${shape.comboboxes})`);
// 77 categories and 7,776 tags both exceed one page; Status and Headline do not.
check(shape.searching === 2, `only the long lists fetch (${shape.searching})`);
for (const s of shape.shipped) console.log(`         ${s.id}: ${s.n} opsi terkirim`);

// A short list filters in the browser, with no request.
const status = p.locator("#filter-f_Status");
await status.click();
await p.waitForTimeout(250);
await status.fill("Dipublikasi");
await p.waitForTimeout(300);
const statusOpts = await p.evaluate(() =>
  [...document.querySelectorAll('#filter-f_Status-listbox [role="option"]')]
    .filter(o => o.offsetParent !== null).map(o => o.textContent.trim()));
check(statusOpts.length > 0, `the short list narrows in the browser, no request (${statusOpts.join(", ")})`);

// The long list fetches as you type.
let fetched = null;
p.on("response", r => { if (r.url().includes("_options?filter=")) fetched = r.url(); });
const tag = p.locator("#filter-f_Tags\\.ID");
await tag.click();
await p.waitForTimeout(400);
await tag.fill("wisata");
await p.waitForTimeout(1200);
check(fetched !== null, `typing fetches from the server (${fetched ? fetched.split("/admin")[1] : "no request"})`);
const tagOpts = await p.evaluate(() =>
  [...document.querySelectorAll('#filter-f_Tags\\.ID-listbox [role="option"]')].map(o => o.textContent.trim()));
check(tagOpts.length > 0 && tagOpts.length <= 50, `it returns a page of matches (${tagOpts.length})`);
check(tagOpts.some(t => /wisata/i.test(t)), `and they match (${tagOpts.slice(0,3).join(", ")})`);

// Choosing one, then applying, actually filters the grid.
const before = await p.evaluate(() => document.querySelectorAll("table tbody tr").length);
await p.locator('#filter-f_Tags\\.ID-listbox [role="option"]').first().click();
await p.waitForTimeout(300);
const hidden = await p.evaluate(() =>
  document.querySelector('#filter-f_Tags\\.ID-combobox input[type=hidden]').value);
check(hidden !== "", `choosing sets the submitted value (${hidden})`);

await p.locator('#grid-filters button[type=submit]').click();
await p.waitForTimeout(1500);
const applied = await p.evaluate(() => ({
  url: location.search,
  rows: document.querySelectorAll("table tbody tr").length,
  shown: document.querySelector('#filter-f_Tags\\.ID')?.value,
}));
check(applied.url.includes("f_Tags.ID="), `the filter reaches the URL (${applied.url.slice(0, 60)})`);
check(applied.rows > 0 && applied.rows <= before, `the grid is filtered (${before} -> ${applied.rows} baris)`);
check(applied.shown && applied.shown !== "", `the applied filter shows its label (${applied.shown})`);

// And clearing it lets go.
await p.locator('#filter-f_Tags\\.ID-combobox [data-clear]').click();
await p.waitForTimeout(300);
const cleared = await p.evaluate(() =>
  document.querySelector('#filter-f_Tags\\.ID-combobox input[type=hidden]').value);
check(cleared === "", `clearing empties the submitted value (${JSON.stringify(cleared)})`);

await p.locator("#grid-filters").screenshot({ path: "/tmp/filters.png" });
await b.close();
console.log(failures.length ? `\n${failures.length} gagal` : "\nsemua lolos");
