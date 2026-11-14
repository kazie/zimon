// The admin page: everything goes through zimon's API with the session
// cookie (or the proxy's token). Every change shows its plan (a ?preview=1
// request) before it runs. Views: Overview, Collections, Your ZIMs, Catalog
// and Jobs, picked by the address (#catalog); each loads when shown, and
// again after a change once it is stale.
"use strict";

const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => document.querySelectorAll(sel);

// el builds an element: el("td", {class: "num"}, "text", childNode, …).
function el(tag, attrs, ...children) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (k.startsWith("on")) e.addEventListener(k.slice(2), v);
    else if (v !== false && v != null) e.setAttribute(k, v === true ? "" : v);
  }
  for (const c of children.flat()) if (c != null) e.append(c);
  return e;
}

function size(n) {
  if (!n) return "0 B";
  const units = ["B", "kB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1000 && i < units.length - 1) { n /= 1000; i++; }
  return (i === 0 || n >= 100 ? n.toFixed(0) : n >= 10 ? n.toFixed(1) : n.toFixed(2)) + " " + units[i];
}

// ago is how long ago an RFC 3339 time was, in words.
function ago(t) {
  const s = (Date.now() - new Date(t)) / 1000;
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
  return new Date(t).toLocaleDateString();
}

// took is how long a job ran, in words ("" before it starts).
function took(j) {
  if (!j.started) return "";
  const s = Math.max(0, ((j.finished ? new Date(j.finished) : Date.now()) - new Date(j.started)) / 1000);
  return s < 60 ? `${Math.round(s)} s` : s < 3600 ? `${Math.round(s / 60)} min` : `${(s / 3600).toFixed(1)} h`;
}

const plural = (n, one, many) => `${n} ${n === 1 ? one : many}`;

// call asks zimon's API and answers [data, response]; api answers the data.
async function call(path, body) {
  const opts = { headers: { "X-Requested-With": "zimon" } };
  if (body !== undefined) {
    opts.method = "POST";
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  const resp = await fetch("/api/v1/" + path, opts);
  if (resp.status === 401) { location.reload(); throw new Error("signed out"); }
  const data = await resp.json();
  if (!resp.ok) throw new Error(data.error || resp.statusText);
  return [data, resp];
}

async function api(path, body) {
  return (await call(path, body))[0];
}

function problem(msg) {
  const p = $("#problem");
  p.textContent = msg || "";
  p.hidden = !msg;
}

const report = (e) => problem(e.message);

// ---- states and badges

const states = {
  missing: "not downloaded", downloading: "downloading", up_to_date: "up to date",
  update_available: "update available", gone_upstream: "gone upstream", unwanted: "not wanted",
  held: "held", not_in_library: "",
};

// tones say how a state looks: fine, going on, worth a look, or wrong.
const tones = {
  up_to_date: "ok", done: "ok",
  update_available: "info", downloading: "info", running: "info", queued: "info",
  missing: "warn", held: "warn", canceled: "warn", interrupted: "warn", unwanted: "warn",
  gone_upstream: "bad", failed: "bad",
};

function badge(state) {
  const label = state in states ? states[state] : state;
  return label ? el("span", { class: "badge " + (tones[state] || "") }, label) : null;
}

const word = (s) => s[0].toUpperCase() + s.slice(1);

// topicLabel turns a catalog category into words: stack_exchange → Stack Exchange.
const topicLabel = (c) => (!c || c === "None" ? "" : c.split("_").filter(Boolean).map(word).join(" "));

// ---- the plan dialog

function planTable(plan) {
  const rows = plan.steps.map((s) => el("tr", {},
    el("td", {}, el("span", { class: "kind " + s.kind }, s.kind)),
    el("td", {}, s.key, s.from ? el("span", { class: "note" }, " (replaces " + s.from.join(", ") + ")") : null),
    el("td", { class: "num" }, s.kind === "remove" ? "frees " + size(s.freed) : size(s.bytes)),
    el("td", { class: "note" }, (s.via || []).join(", "))));
  const parts = [];
  if (plan.problems.length) parts.push(el("p", { class: "problem" }, plan.problems.join(" · ")));
  if (rows.length) parts.push(el("table", { class: "items" }, rows));
  else parts.push(el("p", {}, "Nothing to download or delete."));
  for (const d of plan.deferred) {
    parts.push(el("p", { class: "note" }, `Waits (${d.reason}): ${d.kind} ${d.key}`));
  }
  let summary = `Library ${size(plan.current)} → ${size(plan.after)}`;
  if (plan.budget) summary += ` of a ${size(plan.budget)} budget`;
  summary += ` · download ${size(plan.download)} · ${size(plan.free)} free`;
  parts.push(el("p", { class: "note" }, summary));
  return parts;
}

// confirmPlan shows a plan and resolves true when the user goes ahead.
function confirmPlan(title, plan, edits) {
  const dlg = $("#plan");
  $("#plan-title").textContent = title;
  $("#plan-body").replaceChildren(...planTable(plan));
  $("#plan-ok").disabled = !plan.steps.length && !edits;
  dlg.returnValue = "";
  dlg.showModal();
  return new Promise((done) => dlg.addEventListener("close", () => done(dlg.returnValue === "ok"), { once: true }));
}

// change previews a change, asks, and queues it. The job runs only the
// plan shown (plan.id); if the library changed meanwhile, it fails.
async function change(kind, body, title) {
  problem("");
  try {
    const { plan, edits } = await api(kind + "?preview=1", body);
    if (await confirmPlan(title, plan, edits)) {
      await api(kind, { ...body, plan: plan.id });
      loadJobs();
    }
  } catch (e) {
    report(e);
  }
}

const applyAll = () => change("apply", {}, "Apply library.toml");

// start queues a job that has no plan to show (update, refresh).
const start = (kind) => api(kind, {}).then(loadJobs, report);

// exact is the key that names this flavour and no other: "name:" is the
// version without a flavour, where a bare name means every flavour.
const exact = (x) => x.name + ":" + (x.flavour || "");

// ---- jobs: the list feeds the header, the Overview's Now and the Jobs view

let jobs = [];
const progress = {}; // job id → key → [done, total]
const active = (j) => j.state === "queued" || j.state === "running";

// loadJobs fetches the list and redraws all that shows it: the header, the
// Jobs view and the Overview's Now and Needs you, on screen or not.
async function loadJobs() {
  jobs = await api("jobs?limit=50");
  showActivity();
  renderJobs();
  renderOverview();
}

// showActivity is the header's pill: how many jobs run, and how far the
// running one is.
function showActivity() {
  const on = jobs.filter(active);
  const pill = $("#activity");
  pill.hidden = !on.length;
  if (!on.length) return;
  const running = on.find((j) => j.state === "running");
  const bars = Object.values(running ? progress[running.id] || {} : {});
  const done = bars.reduce((n, [d]) => n + d, 0);
  const total = bars.reduce((n, [, t]) => n + t, 0);
  pill.textContent = `⟳ ${plural(on.length, "job", "jobs")}` + (total ? ` · ${Math.floor((100 * done) / total)}%` : "");
}

function progressBars(id) {
  return Object.entries(progress[id] || {}).map(([key, [done, total]]) =>
    el("div", {}, el("span", { class: "note" }, `${key} ${size(done)} of ${size(total)}`),
      el("div", { class: "bar" }, el("span", { style: `width: ${total ? ((100 * done) / total).toFixed(1) : 0}%` }))));
}

const stop = (j) => el("button", { onclick: () => api(`jobs/${j.id}/cancel`, {}).then(loadJobs, report) }, "Stop");

// activeJob is a running or queued job, with its progress, as the Overview
// and the Jobs view show it.
const activeJob = (j) => el("div", { class: "job" },
  el("div", { class: "line" }, badge(j.state), " ", el("b", {}, j.summary),
    el("span", { class: "note" }, ` · ${j.by || ""} · ${ago(j.created)}`), stop(j)),
  el("div", { "data-progress": j.id }, progressBars(j.id)));

function renderJobs() {
  const open = new Set([...$$("#job-list details[open]")].map((d) => d.dataset.job));
  if (!jobs.length) {
    $("#job-list").replaceChildren(el("tr", {}, el("td", { class: "note" }, "No jobs yet: changes you make here run as jobs, one at a time.")));
    return;
  }
  const head = el("tr", {}, el("th", {}, "State"), el("th", {}, "What"), el("th", {}, "By"),
    el("th", {}, "When"), el("th", { class: "num" }, "Took"), el("th", {}));
  $("#job-list").replaceChildren(head, ...jobs.map((j) => el("tr", {},
    el("td", {}, badge(j.state)),
    el("td", {},
      el("details", { "data-job": j.id, open: open.has(String(j.id)) }, el("summary", {}, j.summary),
        j.error ? el("p", { class: "problem" }, j.error) : null,
        el("pre", { "data-log": j.id }, (j.log || []).join("\n") || "(no log)")),
      active(j) ? el("div", { "data-progress": j.id }, progressBars(j.id)) : null),
    el("td", { class: "note" }, j.by || ""),
    el("td", { class: "note", title: new Date(j.created).toLocaleString() }, ago(j.created)),
    el("td", { class: "num note" }, took(j)),
    el("td", { class: "act" }, active(j) ? stop(j) : null))));
}

function follow() {
  const es = new EventSource("/api/v1/events");
  const timers = new Map(); // one per function, so loadJobs never cancels refresh
  const later = (fn) => { clearTimeout(timers.get(fn)); timers.set(fn, setTimeout(fn, 200)); };
  es.addEventListener("progress", (m) => {
    const ev = JSON.parse(m.data);
    (progress[ev.job] ||= {})[ev.key] = [ev.done, ev.total];
    // Progress and log lines update a job in place; the list is fetched
    // when jobs come and go.
    const places = $$(`[data-progress="${ev.job}"]`);
    for (const p of places) p.replaceChildren(...progressBars(ev.job));
    if (!places.length) later(loadJobs); // a job not shown yet
    showActivity();
  });
  es.addEventListener("log", (m) => {
    const ev = JSON.parse(m.data);
    for (const pre of $$(`[data-log="${ev.job}"]`)) {
      if (pre.textContent === "(no log)") pre.textContent = ev.line;
      else pre.append("\n" + ev.line);
    }
  });
  for (const type of ["queued", "started", "waiting"]) es.addEventListener(type, () => later(loadJobs));
  es.addEventListener("finished", (m) => {
    delete progress[JSON.parse(m.data).job];
    later(refresh);
  });
}

// ---- the header: kiwix-serve

async function loadStatus() {
  const s = await api("status");
  const k = $("#kiwix");
  k.hidden = false;
  k.className = "status " + s.kiwix;
  k.textContent = { up: "kiwix-serve up", down: "kiwix-serve starting", missing: "kiwix-serve missing" }[s.kiwix] || s.kiwix;
  return s;
}

// ---- Overview: storage, what runs now, and what needs you

let overview = null; // what the Overview shows besides the jobs

async function loadOverview() {
  const [budget, schedule, items, info, cols, status] = await Promise.all([
    api("budget"), api("schedule"), api("items"), api("info"), api("collections"), loadStatus()]);
  overview = { budget, schedule, items, info, cols, status };
  renderOverview();
}

function renderOverview() {
  if (!overview) return;
  renderStorage();
  renderNow();
  renderNeeds();
}

function renderStorage() {
  const { budget: b, schedule: sched, cols } = overview;
  const titles = Object.fromEntries(cols.map((c) => [c.id, c.title]));
  const limit = b.budget || b.disk + b.free || 1;
  const uses = b.uses.filter((u) => u.wanted || u.disk).sort((x, y) => y.wanted - x.wanted);
  const sum = uses.reduce((n, u) => n + u.wanted, 0);
  const scale = sum > b.total && sum ? b.total / sum : 1; // a ZIM two collections share counts in both
  const pct = (n) => `${Math.min(100, (100 * n * scale) / limit).toFixed(2)}%`;
  const name = (u) => (u.via === "items" ? "Hand-picked" : titles[u.via] || u.via) + (u.level ? ` (${u.level})` : "");
  const stack = el("div", { class: "stack", title: "wanted, of " + (b.budget ? "the budget" : "the disk") },
    uses.map((u, i) => el("span", { class: "c" + (i % 8), style: `width: ${pct(u.wanted)}`, title: `${name(u)}: ${size(u.wanted)}` })),
    b.extra ? el("span", { class: "extra", style: `width: ${pct(b.extra)}`, title: `not wanted: ${size(b.extra)}` }) : null);
  const legend = el("ul", { class: "legend" },
    uses.map((u, i) => el("li", {}, el("i", { class: "c" + (i % 8) }), name(u), " ",
      el("span", { class: "note" }, size(u.wanted) + (u.disk < u.wanted ? ` (${size(u.disk)} on disk)` : "")))),
    b.extra ? el("li", {}, el("i", { class: "extra" }), "Not wanted ", el("span", { class: "note" }, size(b.extra))) : null);
  const stats = el("p", { class: "stats" },
    el("span", {}, "On disk ", el("b", {}, size(b.disk))),
    el("span", {}, "Wanted ", el("b", {}, size(b.total))),
    el("span", {}, "Free ", el("b", {}, size(b.free))),
    b.budget ? el("span", {}, "Budget ", el("b", {}, size(b.budget))) : null,
    sched.next
      ? el("span", {}, `Updates ${sched.update}, next `, el("b", {}, new Date(sched.next).toLocaleString()))
      : el("span", {}, "No scheduled updates (", el("code", {}, "[schedule] update"), " in the config)"));
  $("#storage").replaceChildren(stack, legend, stats);
}

function renderNow() {
  const on = jobs.filter(active);
  if (on.length) {
    $("#now").replaceChildren(...on.map(activeJob));
    return;
  }
  const last = jobs[0];
  $("#now").replaceChildren(el("p", { class: "note" }, "Nothing running",
    last ? [" · last: ", el("a", { href: "#jobs" }, last.summary), ` ${states[last.state] || last.state} ${ago(last.finished || last.created)}`] : null));
}

function renderNeeds() {
  const { items, info, budget, status } = overview;
  const needs = [];
  const need = (tone, text, ...actions) => needs.push(el("li", { class: tone }, el("span", {}, text), el("span", { class: "act" }, actions)));
  if (!info.catalog_entries) {
    const fetching = jobs.some((j) => (j.kind === "refresh" || j.kind === "update") && active(j));
    need("warn", fetching ? "There is no Kiwix catalog yet; zimon serve is fetching it."
      : "There is no Kiwix catalog yet, so sizes show as 0 B. zimon serve tries again later.",
    fetching ? null : el("button", { onclick: () => start("refresh") }, "Fetch the catalog"));
  }
  const count = (state) => items.filter((it) => it.state === state).length;
  if (count("update_available")) {
    need("info", `${plural(count("update_available"), "update", "updates")} available`,
      el("button", { onclick: () => start("update") }, "Update now"));
  }
  const toApply = [
    count("missing") ? `${plural(count("missing"), "ZIM", "ZIMs")} not downloaded yet` : "",
    budget.extra ? `${size(budget.extra)} on disk that library.toml doesn't want` : "",
  ].filter(Boolean);
  if (toApply.length) {
    need("warn", toApply.join("; "), el("button", { onclick: applyAll }, "Apply…"));
  }
  const last = jobs.find((j) => !active(j));
  if (last && (last.state === "failed" || last.state === "interrupted")) {
    need("bad", `${last.summary} ${last.state}${last.error ? ": " + last.error : ""}`,
      el("a", { href: "#jobs" }, "Show the log"));
  }
  if (status.kiwix === "missing") need("bad", "kiwix-serve is missing, so books can't be opened.");
  $("#needs").replaceChildren(...needs);
  $("#needs-card").hidden = !needs.length;
}

// ---- Collections: a segmented Off / Light / Standard / Deep per collection

const levels = ["light", "standard", "deep"];

async function loadCollections() {
  const cols = await api("collections");
  $("#collection-list").replaceChildren(...cols.map((c) => {
    const now = c.mine ? c.level : "off";
    const pick = (level) => {
      if (level === now) return;
      if (level === "off") return change("remove", { targets: ["@" + c.id] }, `Drop ${c.title}`);
      if (!c.mine) return change("add", { collections: { [c.id]: level } }, `Add ${c.title} at ${level}`);
      return change("level", { target: "@" + c.id, choice: level }, `${c.title}: ${c.level} → ${level}`);
    };
    const option = (level, small) => el("button", { "aria-pressed": String(level === now), onclick: () => pick(level) },
      el("span", {}, word(level)), el("small", {}, small));
    return el("li", { class: c.mine ? "mine" : null },
      el("b", {}, c.title),
      el("p", {}, c.blurb || ""),
      el("div", { class: "seg", role: "group", "aria-label": c.title + " depth" },
        option("off", "—"), levels.map((l) => option(l, size(c.sizes[l])))));
  }));
}

// ---- Your ZIMs: filtered here, as you type

let items = [];

// flavourPicker loads a ZIM's flavours on first use and changes to one.
function flavourPicker(item) {
  const sel = el("select", { "aria-label": "Flavour of " + item.name },
    el("option", { value: "" }, item.flavour || "default"));
  sel.addEventListener("focus", async () => {
    if (sel.options.length > 1) return;
    const entries = await api("entries/" + encodeURIComponent(item.name));
    for (const e of entries) {
      if (e.flavour === item.flavour) continue;
      sel.append(el("option", { value: e.flavour || "default" }, `${e.flavour || "default"} · ${size(e.size)}`));
    }
  }, { once: true });
  sel.addEventListener("change", () => {
    const choice = sel.value;
    sel.value = "";
    if (choice) change("level", { target: item.name, choice }, `${item.name}: ${item.flavour || "default"} → ${choice}`);
  });
  return sel;
}

const via = (it) => (it.via || []).map((v) => (v === "items" ? "hand-picked" : v)).join(", ");

async function loadItems() {
  items = await api("items");
  renderItems();
}

function renderItems() {
  const q = $("#zims-filter").value.trim().toLowerCase();
  const shown = items.filter((it) => !q || [it.key, it.title, via(it)].some((s) => (s || "").toLowerCase().includes(q)));
  const head = el("tr", {}, el("th", {}, "ZIM"), el("th", {}, "State"), el("th", {}, "Why"),
    el("th", { class: "num" }, "Size"), el("th", {}, "Flavour"), el("th", {}));
  $("#items").replaceChildren(head, ...shown.map((it) => el("tr", {},
    el("td", {}, el("b", {}, it.key), it.title ? el("div", { class: "note" }, it.title) : null),
    el("td", {}, badge(it.state)),
    el("td", { class: "note" }, via(it)),
    el("td", { class: "num" }, it.size ? size(it.size) : ""),
    el("td", {}, flavourPicker(it)),
    el("td", { class: "act" }, el("button", { onclick: () => change("remove", { targets: [exact(it)] }, "Remove " + it.key) }, "Remove")))));
  const total = size(items.reduce((n, it) => n + (it.size || 0), 0));
  $("#zims-found").textContent = q ? `${shown.length} of ${plural(items.length, "ZIM", "ZIMs")}` : `${plural(items.length, "ZIM", "ZIMs")}, ${total}`;
}

$("#zims-filter").addEventListener("input", renderItems);

// ---- Catalog: a filter over all of it, as you type, by language and
// topic. Empty, it lists everything (by name), a page at a time.

const page = 200;    // rows per page
const maxRows = 1000; // the most the API answers at once
let shown = page;
let asked = 0;        // the newest request: an older answer that comes late is dropped

// loadFacets fills the language and topic choices once.
let facets = null;
const loadFacets = () => (facets ||= fillFacets().catch((e) => { facets = null; report(e); }));

async function fillFacets() {
  const [langs, topics] = await Promise.all([api("languages"), api("categories")]);
  $("#lang").append(...langs.map((f) => el("option", { value: f.value }, `${f.value} (${f.count.toLocaleString()})`)));
  $("#topic").append(...topics.filter((f) => topicLabel(f.value)).map((f) =>
    el("option", { value: f.value }, `${topicLabel(f.value)} (${f.count.toLocaleString()})`)));
}

async function loadCatalog() {
  const q = $("#filter").value.trim();
  const params = new URLSearchParams({ q, limit: shown });
  if ($("#lang").value) params.set("lang", $("#lang").value);
  if ($("#topic").value) params.set("category", $("#topic").value);
  const mine = ++asked;
  loadFacets();
  const [found, resp] = await call("search?" + params);
  if (mine !== asked) return;
  const total = Number(resp.headers.get("X-Total-Count")) || found.length;
  const head = el("tr", {}, el("th", {}, "ZIM"), el("th", {}, "Language"), el("th", {}, "Topic"),
    el("th", { class: "num" }, "Size"), el("th", {}, "State"), el("th", {}));
  $("#results").replaceChildren(head, ...found.map((e) => el("tr", {},
    el("td", {}, el("b", {}, e.key), el("div", { class: "note" }, e.title + (e.summary ? " — " + e.summary : ""))),
    el("td", {}, e.language),
    el("td", { class: "note" }, topicLabel(e.category)),
    el("td", { class: "num" }, size(e.size)),
    el("td", {}, badge(e.state)),
    el("td", { class: "act" }, e.state === "not_in_library"
      ? el("button", { onclick: () => change("add", { keys: [exact(e)] }, "Add " + e.key) }, "Add") : null))));
  const filtered = q || params.has("lang") || params.has("category");
  $("#found").textContent = !total ? (filtered ? "Nothing matches." : "The catalog is empty.")
    : `${found.length.toLocaleString()} of ${total.toLocaleString()} ZIMs` +
      (q ? ` matching “${q}”` : "") + (params.has("lang") || params.has("category") ? " (filtered)" : "");
  $("#more-row").hidden = found.length >= total;
  $("#more").hidden = shown >= maxRows;
  $("#more-note").textContent = shown >= maxRows ? `The first ${maxRows.toLocaleString()}: type to narrow it down.` : "";
}

let typing;
const refilter = () => { shown = page; loadCatalog().catch(report); };
$("#filter").addEventListener("input", () => { clearTimeout(typing); typing = setTimeout(refilter, 200); });
$("#lang").addEventListener("change", refilter);
$("#topic").addEventListener("change", refilter);
$("#more").addEventListener("click", () => {
  shown = Math.min(shown + page, maxRows);
  loadCatalog().catch(report);
});

// ---- views: the open one is in the address (#catalog), so a reload, a
// link or the back button keeps it. A view loads when shown, and after a
// change, again, once it is stale.

const views = {
  overview: loadOverview, collections: loadCollections, zims: loadItems,
  catalog: loadCatalog, jobs: async () => renderJobs(), // loadJobs keeps the Jobs view current
};
const stale = new Set(Object.keys(views));
let current = "";

function showView(name) {
  if (!(name in views)) name = "overview";
  if (name !== current) window.scrollTo(0, 0);
  current = name;
  for (const s of $$("section[data-view]")) s.hidden = s.dataset.view !== name;
  for (const a of $$("nav.views a")) a.classList.toggle("on", a.dataset.view === name);
  if (stale.delete(name)) views[name]().catch(report);
}

// refresh is what a finished job sets off: every view is stale, the one
// on screen loads now.
async function refresh() {
  for (const v of Object.keys(views)) stale.add(v);
  stale.delete(current);
  await Promise.all([loadJobs(), loadStatus(), views[current]()]).catch(report);
}

window.addEventListener("hashchange", () => showView(location.hash.slice(1)));

// ---- the buttons at the top

$("#apply").addEventListener("click", applyAll);
$("#update").addEventListener("click", () => start("update"));

showView(location.hash.slice(1));
loadJobs().catch(report);
loadStatus().catch(report);
// kiwix-serve has no event of its own: look every 15 s while the page shows.
setInterval(() => document.hidden || loadStatus().catch(report), 15000);
document.addEventListener("visibilitychange", () => document.hidden || loadStatus().catch(report));
follow();
