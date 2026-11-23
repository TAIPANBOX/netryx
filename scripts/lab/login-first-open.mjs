// The first open of the console with no session shows the login form: a
// fresh headless browser loads the page, and every script it fetches answers
// 200. Runs on forge against the lab console through the tunnel:
//
//   ssh -N -L 18788:127.0.0.1:8788 root@<lab router> &
//   node scripts/lab/login-first-open.mjs http://127.0.0.1:18788
import { chromium } from "playwright";

const base = process.argv[2] || "http://127.0.0.1:18788";
const browser = await chromium.launch();
const ctx = await browser.newContext({ ignoreHTTPSErrors: true });
const page = await ctx.newPage();
const failed = [];
const errors = [];
page.on("response", (r) => {
  if (r.url().endsWith(".js") && r.status() !== 200) failed.push(`${r.status()} ${new URL(r.url()).pathname}`);
});
page.on("pageerror", (e) => errors.push(String(e)));
await page.goto(base + "/", { waitUntil: "networkidle" });
const form = await page.locator("input[type=password]").first().isVisible({ timeout: 10000 }).catch(() => false);
const stuck = (await page.evaluate(() => document.body.innerText)).includes("Loading the console");
await browser.close();
console.log(`login form shown: ${form}; still loading: ${stuck}; scripts not 200: ${failed.length ? failed.join(", ") : "none"}; page errors: ${errors.length ? errors.join("; ") : "none"}`);
process.exit(form && !stuck && failed.length === 0 && errors.length === 0 ? 0 : 1);
