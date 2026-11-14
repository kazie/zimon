// Takes the README's screenshots of zimon serve through Chromium's DevTools
// protocol: node tools/serve-shots.mjs DEVTOOLS_PORT BOOKS_URL ADMIN_URL TOKEN.
// tools/serve-shots.sh sets it all up; see there.
import { writeFileSync } from "node:fs";

const [port, books, admin, token] = process.argv.slice(2);
const width = 1280;

const target = await (await fetch(`http://127.0.0.1:${port}/json/new?about:blank`, { method: "PUT" })).json();
const ws = new WebSocket(target.webSocketDebuggerUrl);
await new Promise((ok) => (ws.onopen = ok));
let id = 0;
const waiting = new Map();
ws.onmessage = (m) => {
  const msg = JSON.parse(m.data);
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
async function until(expression) {
  for (let i = 0; i < 100; i++) {
    if (await evaluate(expression)) return;
    await sleep(100);
  }
  throw new Error("timed out waiting for " + expression);
}

// shot saves the page, height pixels of it, as docs/img/name.png.
async function shot(name, height) {
  await send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: 1, mobile: false });
  await sleep(300); // layout and fonts settle
  const { data } = await send("Page.captureScreenshot", { format: "png" });
  writeFileSync(`docs/img/${name}.png`, Buffer.from(data, "base64"));
  console.log(`docs/img/${name}.png`);
}

await send("Page.enable");
await send("Runtime.enable");
await send("Network.enable");
await send("Emulation.setDeviceMetricsOverride", { width, height: 900, deviceScaleFactor: 1, mobile: false });

// The start page: the demo library, by collection.
await send("Page.navigate", { url: books });
await until(`document.querySelectorAll("main section").length > 0`);
await shot("serve-portal", 880);

// The admin page, signed in by the header a proxy would send.
await send("Network.setExtraHTTPHeaders", { headers: { "X-Zimon-Admin-Token": token } });
await send("Page.navigate", { url: admin + "/admin" });
await until(`document.querySelectorAll("#collections li").length > 0 && document.querySelector("#disk").textContent !== ""`);
await shot("serve-admin", 1000);

// The Catalog tab, filtered as you type.
await evaluate(`document.querySelector('[data-tab="catalog"]').click()`);
await until(`document.querySelector("#found").textContent !== ""`);
await evaluate(`(() => { const f = document.querySelector("#filter"); f.value = "wiki"; f.dispatchEvent(new Event("input")); })()`);
await until(`document.querySelector("#found").textContent.includes("matching")`);
await evaluate(`document.activeElement.blur()`);
await shot("serve-catalog", 760);

ws.close();
process.exit(0);
