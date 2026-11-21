// A headless tour of a running Autotaggerr: signs in, visits each page, and saves a
// full-page screenshot per page and width. Fails when a page logs a console error,
// throws, or gets a 5xx — so a tour is a smoke test as well as a picture. See
// docs/demo.md.
//
//   node tour.mjs [--base http://localhost:8090] [--only graph,stats] [--width 1440,390]
//
// Uses playwright-core with an already-installed Chromium: CHROMIUM_PATH, or the newest
// headless shell in Playwright's cache. Nothing is downloaded.
import { chromium } from "playwright-core";
import { existsSync, mkdirSync, readdirSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

const args = Object.fromEntries(
  process.argv.slice(2).reduce((pairs, arg, i, all) => {
    if (arg.startsWith("--")) pairs.push([arg.slice(2), all[i + 1]?.startsWith("--") ? "1" : (all[i + 1] ?? "1")]);
    return pairs;
  }, []),
);
const base = (args.base ?? process.env.TOUR_BASE ?? "http://localhost:8090").replace(/\/$/, "");
const user = args.user ?? "admin";
const password = args.password ?? "admin";
const widths = (args.width ?? "1440,390").split(",").map(Number);
const only = args.only ? new Set(args.only.split(",")) : null;
const out = args.out ?? new URL("./out/", import.meta.url).pathname;

function chromiumPath() {
  if (process.env.CHROMIUM_PATH) return process.env.CHROMIUM_PATH;
  const cache = process.env.PLAYWRIGHT_BROWSERS_PATH ?? join(homedir(), ".cache", "ms-playwright");
  const shells = existsSync(cache) ? readdirSync(cache).filter((d) => d.startsWith("chromium_headless_shell-")).sort() : [];
  for (const dir of shells.reverse()) {
    for (const sub of readdirSync(join(cache, dir))) {
      const bin = join(cache, dir, sub, "chrome-headless-shell");
      if (existsSync(bin)) return bin;
    }
  }
  throw new Error(`no Chromium found: set CHROMIUM_PATH, or install one with "npx playwright-core install chromium-headless-shell"`);
}

async function api(path, token, init = {}) {
  const res = await fetch(`${base}/api/v1${path}`, {
    ...init,
    headers: { "Content-Type": "application/json", ...(token ? { Authorization: `Bearer ${token}` } : {}), ...init.headers },
  });
  if (!res.ok) throw new Error(`${path}: HTTP ${res.status} ${await res.text()}`);
  return res.json();
}

const { token } = await api("/auth/login", null, { method: "POST", body: JSON.stringify({ username: user, password }) });
const artists = await api("/artists", token).catch(() => []);
const firstArtist = Array.isArray(artists) && artists.length ? artists[0].mb_id : null;

/** The tour. `then` runs after the page's screenshot, for interactions worth a picture. */
const stops = [
  { name: "overview", path: "/" },
  { name: "collection", path: "/collection" },
  ...(firstArtist ? [{ name: "artist", path: `/collection/${firstArtist}` }] : []),
  { name: "items", path: "/items" },
  { name: "duplicates", path: "/insights" },
  { name: "stats", path: "/insights?tab=stats" },
  {
    name: "graph",
    path: "/insights?tab=graph",
    async then(page, shot) {
      const nodes = page.locator(".g-node");
      if ((await nodes.count()) === 0) return;
      // The largest node: the one drawn with the biggest radius.
      const radii = await page.$$eval(".g-node circle:not(.hit)", (cs) => cs.map((c) => Number(c.getAttribute("r"))));
      // The circle, not the group: the group's box includes its label, and its centre
      // can fall outside the node.
      const biggest = nodes.nth(radii.indexOf(Math.max(...radii))).locator("circle:not(.hit)");
      await biggest.hover();
      await shot("graph-hover");
      await biggest.click();
      await shot("graph-selected");
    },
  },
  { name: "storage", path: "/insights?tab=storage" },
  { name: "activity", path: "/activity" },
  { name: "settings", path: "/settings" },
].filter((s) => !only || only.has(s.name));

mkdirSync(out, { recursive: true });
const browser = await chromium.launch({ executablePath: chromiumPath(), headless: true });
const problems = [];
try {
  for (const width of widths) {
    const context = await browser.newContext({ viewport: { width, height: width < 600 ? 844 : 900 }, colorScheme: "dark" });
    const page = await context.newPage();
    let stop = "sign-in";
    page.on("console", (m) => m.type() === "error" && problems.push(`${stop}@${width}: console: ${m.text()}`));
    page.on("pageerror", (e) => problems.push(`${stop}@${width}: page error: ${e.message}`));
    page.on("response", (r) => r.status() >= 500 && problems.push(`${stop}@${width}: HTTP ${r.status()} ${r.url()}`));

    // The SPA reads a token from the URL fragment once and keeps it (consumeTokenFromUrl).
    await page.goto(`${base}/#token=${encodeURIComponent(token)}`, { waitUntil: "networkidle" });
    for (const s of stops) {
      stop = s.name;
      await page.goto(`${base}${s.path}`, { waitUntil: "networkidle" });
      // Let skeletons resolve and layouts settle after the last response.
      await page.waitForTimeout(400);
      const shot = async (name) => {
        const file = join(out, `${name}@${width}.png`);
        await page.screenshot({ path: file, fullPage: true });
        console.log(file);
      };
      await shot(s.name);
      if (s.then) await s.then(page, shot);
    }
    await context.close();
  }
} finally {
  await browser.close();
}
if (problems.length) {
  console.error(`\n${problems.length} problem(s):\n` + problems.map((p) => `  ${p}`).join("\n"));
  process.exit(1);
}
