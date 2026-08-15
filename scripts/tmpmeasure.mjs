import { webkit } from "playwright";
const b = await webkit.launch();
const p = await b.newPage();
await p.goto("http://localhost:8080/admin/auth/login");
await p.fill("#login-username", "developer");
await p.fill("#login-password", "qwerty123");
await Promise.all([p.waitForNavigation(), p.click("button[type=submit]")]);

let bytes = 0;
p.on("response", async r => {
  if (r.url().includes("/admin/berita") && !r.url().includes("_assets")) {
    try { bytes += (await r.body()).length; } catch {}
  }
});
const t0 = Date.now();
await p.goto("http://localhost:8080/admin/berita");
await p.waitForSelector("table");
const ms = Date.now() - t0;

const m = await p.evaluate(() => {
  const opts = [...document.querySelectorAll("#grid-filters select")].map(s => ({
    name: s.name, n: s.options.length,
  }));
  return { selects: opts, totalOptions: opts.reduce((a, o) => a + o.n, 0),
    html: document.documentElement.outerHTML.length };
});
console.log(`halaman grid : ${(bytes/1024).toFixed(0)} KB terunduh, DOM ${(m.html/1024).toFixed(0)} KB, muat ${ms} ms`);
for (const s of m.selects) console.log(`  select ${s.name.padEnd(14)} ${s.n} opsi`);
console.log(`  total          ${m.totalOptions} opsi dalam HTML`);
await b.close();
