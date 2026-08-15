import { webkit } from "playwright";
const failures = [];
const check = (ok, m) => { console.log((ok ? "  ok   " : "  FAIL ") + m); if (!ok) failures.push(m); };
const b = await webkit.launch();
const p = await b.newPage({ viewport: { width: 1400, height: 1100 } });
await p.goto("http://localhost:8080/admin/auth/login");
await p.fill("#login-username", "developer");
await p.fill("#login-password", "qwerty123");
await Promise.all([p.waitForNavigation(), p.click("button[type=submit]")]);
await p.goto("http://localhost:8080/admin/berita/105411");
await p.waitForSelector(".steward-detail");

const m = await p.evaluate(() => {
  const rows = [...document.querySelectorAll(".steward-detail-row")];
  const dt = rows[0].querySelector("dt"), dd = rows[0].querySelector("dd");
  const cs = n => getComputedStyle(n);
  const withBorder = rows.filter(r => cs(r).borderBottomWidth !== "0px").length;
  const blocks = rows.filter(r => r.classList.contains("steward-detail-row-block"));
  // Widest empty gap: what the two-column grid used to leave beside a long value.
  return {
    rows: rows.length,
    separators: withBorder,
    lastHasBorder: cs(rows[rows.length - 1]).borderBottomWidth !== "0px",
    labelSize: cs(dt).fontSize, valueSize: cs(dd).fontSize,
    labelWeight: cs(dt).fontWeight, valueWeight: cs(dd).fontWeight,
    labelColour: cs(dt).color, valueColour: cs(dd).color,
    blocks: blocks.map(r => r.querySelector("dt").textContent.trim()),
    blockCols: blocks.length ? cs(blocks[0]).gridTemplateColumns : "n/a",
    normalCols: cs(rows[0]).gridTemplateColumns,
    copyButtons: document.querySelectorAll(".steward-detail [data-steward-copy]").length,
  };
});
check(m.separators === m.rows - 1 && !m.lastHasBorder,
  `every row but the last has a rule (${m.separators}/${m.rows})`);
check(m.labelSize !== m.valueSize || m.labelWeight !== m.valueWeight,
  `label and value differ in more than colour (${m.labelSize}/${m.labelWeight} vs ${m.valueSize}/${m.valueWeight})`);
check(m.normalCols.split(" ").length === 2, `a short field is label-beside-value (${m.normalCols})`);
check(m.blocks.includes("Konten"), `the article body is a block row (${m.blocks.join(", ")})`);
check(m.blockCols.split(" ").length === 1, `and takes the full width (${m.blockCols})`);
check(m.copyButtons === 1, `the copyable field has a button (${m.copyButtons})`);

// The button appears on hover and actually copies.
await p.locator(".steward-detail-row").first().hover();
await p.waitForTimeout(200);
const vis = await p.evaluate(() => {
  const btn = document.querySelector(".steward-detail [data-steward-copy]");
  return { opacity: getComputedStyle(btn).opacity, value: btn.getAttribute("data-steward-copy") };
});
check(vis.opacity === "1", `it shows on hover (opacity ${vis.opacity})`);
check(vis.value === "105411", `and carries the stored value (${vis.value})`);

await p.context().grantPermissions(["clipboard-read", "clipboard-write"]).catch(() => {});
await p.locator(".steward-detail [data-steward-copy]").click();
await p.waitForTimeout(500);
const after = await p.evaluate(async () => {
  const btn = document.querySelector(".steward-detail [data-steward-copy]");
  let clip = null;
  try { clip = await navigator.clipboard.readText(); } catch {}
  return { flashed: btn.hasAttribute("data-copied"), clip,
    toast: !!document.querySelector("[data-slot=toast], .toast") };
});
check(after.flashed || after.toast, `pressing it confirms (flash ${after.flashed}, toast ${after.toast})`);
if (after.clip !== null) check(after.clip === "105411", `the clipboard holds the value (${after.clip})`);

await p.locator(".steward-detail").screenshot({ path: "/tmp/detail-after.png" });
await b.close();
console.log(failures.length ? `\n${failures.length} gagal` : "\nsemua lolos");
