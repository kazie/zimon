// Checks the admin page and takes the README's screenshots of zimon serve,
// through Chromium's DevTools protocol:
//   node tools/serve-shots.mjs DEVTOOLS_PORT BOOKS_URL ADMIN_URL TOKEN
// Every view, at a desktop and a phone width, light and dark: no script
// errors, no sideways scrolling, its content there. Then the shots, into
// docs/img. tools/serve-shots.sh sets it all up; see there.
import { writeFileSync } from "node:fs";

const [port, books, admin, token] = process.argv.slice(2);

const target = await (await fetch(`http://127.0.0.1:${port}/json/new?about:blank`, { method: "PUT" })).json();
const ws = new WebSocket(target.webSocketDebuggerUrl);
await new Promise((ok) => (ws.onopen = ok));
let id = 0;
const waiting = new Map();
const errors = [];
ws.onmessage = (m) => {
  const msg = JSON.parse(m.data);
  if (msg.method === "Runtime.exceptionThrown") {
    errors.push(msg.params.exceptionDetails.exception?.description || msg.params.exceptionDetails.text);
  }
  if (waiting.has(msg.id)) {
    const { ok, fail } = waiting.get(msg.id);
    waiting.delete(msg.id);
    msg.error ? fail(new Error(msg.error.message)) : ok(msg.result);
  }
};
const send = (method, params = {}) => new Promise((ok, fail) => {
  waiting.set(++id, { ok, fail });
  ws.send(JSON.stringify({ id, method, params }));
});
const sleep = (ms) => new Promise((ok) => setTimeout(ok, ms));
const evaluate = async (expression) => (await send("Runtime.evaluate", { expression, awaitPromise: true })).result.value;

// until waits for a page condition (a JS expression) to hold.
async function until(expression, what) {
  for (let i = 0; i < 100; i++) {
    if (await evaluate(expression)) return;
    await sleep(100);
  }
  throw new Error(`timed out waiting for ${what || expression}`);
}

const size = (width, height) => send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: 1, mobile: width < 600 });
const scheme = (value) => send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value }] });

// shot saves what is on screen, width × height, as docs/img/name.png.
async function shot(name, width, height) {
  await size(width, height);
  await evaluate("window.scrollTo(0, 0)");
  await sleep(300); // layout and fonts settle
  const { data } = await send("Page.captureScreenshot", { format: "png" });
  writeFileSync(`docs/img/${name}.png`, Buffer.from(data, "base64"));
  console.log(`docs/img/${name}.png`);
}

// What each admin view shows once it has loaded.
const loaded = {
  overview: `document.querySelector("#storage .stack") && document.querySelector("#now").textContent !== ""`,
  collections: `document.querySelectorAll("#collection-list .seg").length > 0`,
  zims: `document.querySelectorAll("#items tr").length > 1`,
  catalog: `document.querySelectorAll("#results tr").length > 1`,
  jobs: `document.querySelectorAll("#job-list tr").length > 1`,
};

async function view(name) {
  await evaluate(`location.hash = "#${name}"`);
  await until(`!document.querySelector('section[data-view="${name}"]').hidden && (${loaded[name]})`, name);
}

await send("Page.enable");
await send("Runtime.enable");
await send("Network.enable");
await scheme("light");
await size(1280, 900);

// The start page: the demo library, by collection.
await send("Page.navigate", { url: books });
await until(`document.querySelectorAll("main section").length > 0`, "the start page");
await shot("serve-portal", 1280, 880);

// The admin page, signed in by the header a proxy would send.
await send("Network.setExtraHTTPHeaders", { headers: { "X-Zimon-Admin-Token": token } });
await send("Page.navigate", { url: admin + "/admin#overview" });
await until(`typeof showView === "function"`, "the admin page");

const problems = [];
for (const colours of ["light", "dark"]) {
  await scheme(colours);
  for (const width of [1280, 390]) {
    await size(width, 900);
    for (const name of Object.keys(loaded)) {
      await view(name);
      await sleep(150);
      const wide = await evaluate(`document.documentElement.scrollWidth - document.documentElement.clientWidth`);
      if (wide > 0) problems.push(`${name}, ${width} px, ${colours}: ${wide} px wider than the window`);
    }
  }
}
if (errors.length) problems.push(...errors.map((e) => "script error: " + e));
const shown = await evaluate(`document.querySelector("#problem").hidden ? "" : document.querySelector("#problem").textContent`);
if (shown) problems.push("the page shows a problem: " + shown);
if (problems.length) {
  console.error(problems.join("\n"));
  process.exit(1);
}
console.log("every view: no script errors, no sideways scrolling, light and dark, 1280 and 390 px");

// The shots, light.
await scheme("light");
await size(1280, 900);
await view("overview");
await shot("serve-admin", 1280, 820);
await view("collections");
await shot("serve-collections", 1280, 760);
await view("catalog");
await evaluate(`(() => { const f = document.querySelector("#filter"); f.value = "wiki"; f.dispatchEvent(new Event("input")); })()`);
await until(`document.querySelector("#found").textContent.includes("matching")`, "the filtered catalog");
await evaluate(`document.activeElement.blur()`);
await shot("serve-catalog", 1280, 760);

ws.close();
process.exit(0);
