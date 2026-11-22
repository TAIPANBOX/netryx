// Recaptures docs/images/*.png from a running netryx console.
//
//   ssh -N -L 18788:127.0.0.1:8788 root@<lab router> &
//   node scripts/lab/console-shots.mjs docs/images
//
// It signs in as the lab login netryxlab with the password in ~/netryx-shots.pw
// (0600), once, and reuses the cookie for every view. Run it against a lab house
// (lab/fakehouse.sh), never a real one: the images are published.
import { chromium } from "playwright";
import fs from "node:fs";
const BASE = "http://127.0.0.1:18788";
const OUT = process.argv[2] || "shots";
const pw = fs.readFileSync(process.env.HOME + "/netryx-shots.pw", "utf8").trim();
fs.mkdirSync(OUT, { recursive: true });
const browser = await chromium.launch();
const shots = [
  ["overview", "#/", 1440, 1000],
  ["clients", "#/clients", 1440, 900],
  ["client", "#/client/02:4e:58:00:00:03", 1440, 1400],
  ["flows", "#/flows?q=" + encodeURIComponent('client:"Sam\'s phone"'), 1440, 900],
  ["rules", "#/rules", 1440, 900],
  ["security", "#/security", 1440, 1000],
  ["apps", "#/apps", 1440, 1000],
  ["destinations", "#/destinations", 1440, 1000],
  ["network", "#/network", 1440, 1000],
];
// One login; every view reuses its cookie.
const first = await browser.newContext();
const r = await first.request.post(BASE + "/api/login", { data: { user: "netryxlab", password: pw } });
if (!r.ok()) throw new Error("login " + r.status());
const state = await first.storageState();
await first.close();
for (const [name, hash, w, h, click] of shots) {
  const ctx = await browser.newContext({ viewport: { width: w, height: h }, deviceScaleFactor: 2, storageState: state });
  const page = await ctx.newPage();
  await page.goto(BASE + "/" + hash, { waitUntil: "networkidle" });
  await page.waitForTimeout(3500);
  if (click) {
    await page.getByRole("button", { name: click, exact: true }).click();
    await page.waitForTimeout(3500);
  }
  const login = await page.locator("input[type=password]").count();
  if (login) throw new Error(name + ": the login page is showing");
  await page.screenshot({ path: `${OUT}/${name}.png`, fullPage: process.env.FULL === "1" });
  await ctx.close();
  console.log("saved", name);
}
await browser.close();
