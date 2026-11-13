// The admin page: everything goes through zimon's API with the session
// cookie. Every change shows its plan (a ?preview=1 request) before it runs.
"use strict";

const $ = (sel) => document.querySelector(sel);

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

async function api(path, body) {
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
  return data;
}

function problem(msg) {
  const p = $("#problem");
  p.textContent = msg || "";
  p.hidden = !msg;
}

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
    problem(e.message);
  }
}

// exact is the key that names this flavour and no other: "name:" is the
// version without a flavour, where a bare name means every flavour.
const exact = (x) => x.name + ":" + (x.flavour || "");

// ---- disk

async function loadDisk() {
  const [b, sched] = await Promise.all([api("budget"), api("schedule")]);
  const limit = b.budget || b.disk + b.free;
  const children = [
    el("span", {}, "On disk ", el("b", {}, size(b.disk))),
    el("span", {}, "Wanted ", el("b", {}, size(b.total))),
    el("span", {}, "Free ", el("b", {}, size(b.free))),
  ];
  if (b.budget) children.push(el("span", {}, "Budget ", el("b", {}, size(b.budget))));
  if (b.extra) children.push(el("span", {}, "Not wanted ", el("b", {}, size(b.extra)), " (Apply removes it)"));
  children.push(sched.next
    ? el("span", {}, `Updates ${sched.update}, next `, el("b", {}, new Date(sched.next).toLocaleString()))
    : el("span", {}, "No scheduled updates (", el("code", {}, "[schedule] update"), " in the config)"));
  const used = el("span", { style: `width: ${Math.min(100, (100 * b.total) / limit).toFixed(1)}%` });
  children.push(el("div", { class: "bar", title: "wanted, of " + (b.budget ? "the budget" : "the disk") }, used));
  $("#disk").replaceChildren(...children);
}

// ---- collections

const levels = ["light", "standard", "deep"];

async function loadCollections() {
  const cols = await api("collections");
  $("#collections").replaceChildren(...cols.map((c) => {
    const pick = (level) => {
      if (level === (c.mine ? c.level : "off")) return;
      if (level === "off") return change("remove", { targets: ["@" + c.id] }, `Drop ${c.title}`);
      if (!c.mine) return change("add", { collections: { [c.id]: level } }, `Add ${c.title} at ${level}`);
      return change("level", { target: "@" + c.id, choice: level }, `${c.title}: ${c.level} → ${level}`);
    };
    const button = (level, label) => el("button", { class: (c.mine ? c.level : "off") === level ? "on" : null, onclick: () => pick(level) }, label);
    return el("li", {},
      el("b", {}, c.title),
      el("p", {}, c.blurb || ""),
      el("div", { class: "levels" },
        button("off", "Off"),
        levels.map((l) => button(l, `${l[0].toUpperCase() + l.slice(1)} · ${size(c.sizes[l])}`))));
  }));
}

// ---- your ZIMs

const states = {
  missing: "not downloaded", downloading: "downloading", up_to_date: "up to date",
  update_available: "update available", gone_upstream: "gone upstream", unwanted: "not wanted",
  held: "held",
};

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

async function loadItems() {
  const items = await api("items");
  const head = el("tr", {}, el("th", {}, "ZIM"), el("th", {}, "State"), el("th", {}, "Why"), el("th", { class: "num" }, "Size"), el("th", {}, "Flavour"), el("th", {}));
  $("#items").replaceChildren(head, ...items.map((it) => el("tr", {},
    el("td", {}, el("b", {}, it.key), it.title ? el("div", { class: "note" }, it.title) : null),
    el("td", {}, states[it.state] || it.state),
    el("td", { class: "note" }, (it.via || []).map((v) => (v === "items" ? "hand-picked" : v)).join(", ")),
    el("td", { class: "num" }, it.size ? size(it.size) : ""),
    el("td", {}, flavourPicker(it)),
    el("td", { class: "act" }, el("button", { onclick: () => change("remove", { targets: [exact(it)] }, "Remove " + it.key) }, "Remove")))));
}

// ---- the catalog

$("#search").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const q = new FormData(ev.target).get("q");
  try {
    const found = await api("search?q=" + encodeURIComponent(q) + "&limit=50");
    $("#results").replaceChildren(...found.map((e) => el("tr", {},
      el("td", {}, el("b", {}, e.key), el("div", { class: "note" }, e.title + (e.summary ? " — " + e.summary : ""))),
      el("td", {}, e.language),
      el("td", { class: "num" }, size(e.size)),
      el("td", {}, states[e.state] || ""),
      el("td", { class: "act" }, e.state === "not_in_library"
        ? el("button", { onclick: () => change("add", { keys: [exact(e)] }, "Add " + e.key) }, "Add") : null))));
    if (!found.length) $("#results").replaceChildren(el("tr", {}, el("td", { class: "note" }, "Nothing found.")));
  } catch (e) {
    problem(e.message);
  }
});

// ---- jobs, live

const progress = {}; // job id → key → [done, total]

async function loadJobs() {
  const jobs = await api("jobs?limit=15");
  if (!jobs.length) {
    $("#jobs").replaceChildren(el("li", { class: "note" }, "No jobs yet: changes you make here run as jobs, one at a time."));
    return;
  }
  $("#jobs").replaceChildren(...jobs.map((j) => {
    const bars = el("div", { id: "progress-" + j.id }, progressBars(j.id));
    const active = j.state === "queued" || j.state === "running";
    return el("li", {},
      el("span", { class: "state " + j.state }, j.state),
      " ", j.summary, el("span", { class: "note" }, ` · ${new Date(j.created).toLocaleString()}${j.by ? " · " + j.by : ""}`),
      active ? el("button", { onclick: () => api(`jobs/${j.id}/cancel`, {}).then(loadJobs, (e) => problem(e.message)) }, "Stop") : null,
      j.error ? el("div", { class: "problem" }, j.error) : null,
      bars,
      logView(j.id, j.log || []));
  }));
}

const lines = (n) => `${n} line${n === 1 ? "" : "s"}`;

// logView is a job's log, folded; appendLog adds lines to it as they come.
function logView(id, log) {
  return el("details", { id: "log-" + id, hidden: !log.length },
    el("summary", {}, lines(log.length)), el("pre", {}, log.join("\n")));
}

function appendLog(id, line) {
  const d = document.getElementById("log-" + id);
  if (!d) return false;
  const pre = d.querySelector("pre");
  pre.textContent += (pre.textContent ? "\n" : "") + line;
  d.querySelector("summary").textContent = lines(pre.textContent.split("\n").length);
  d.hidden = false;
  return true;
}

function progressBars(id) {
  return Object.entries(progress[id] || {}).map(([key, [done, total]]) =>
    el("div", {}, el("span", { class: "note" }, `${key} ${size(done)} of ${size(total)}`),
      el("div", { class: "bar" }, el("span", { style: `width: ${total ? ((100 * done) / total).toFixed(1) : 0}%` }))));
}

function follow() {
  const es = new EventSource("/api/v1/events");
  const timers = new Map(); // one per function, so loadJobs never cancels loadAll
  const later = (fn) => { clearTimeout(timers.get(fn)); timers.set(fn, setTimeout(fn, 200)); };
  es.addEventListener("progress", (m) => {
    const ev = JSON.parse(m.data);
    (progress[ev.job] ||= {})[ev.key] = [ev.done, ev.total];
    // Progress and log lines update the job in place; the list is fetched
    // when jobs come and go.
    const bars = document.getElementById("progress-" + ev.job);
    if (bars) bars.replaceChildren(...progressBars(ev.job));
    else later(loadJobs);
  });
  es.addEventListener("log", (m) => {
    const ev = JSON.parse(m.data);
    if (!appendLog(ev.job, ev.line)) later(loadJobs);
  });
  for (const type of ["queued", "started", "waiting"]) es.addEventListener(type, () => later(loadJobs));
  es.addEventListener("finished", (m) => {
    delete progress[JSON.parse(m.data).job];
    later(loadAll);
  });
}

// ---- the buttons at the top

$("#apply").addEventListener("click", () => change("apply", {}, "Apply library.toml"));

$("#update").addEventListener("click", () => api("update", {}).then(loadJobs, (e) => problem(e.message)));

async function loadAll() {
  try {
    await Promise.all([loadDisk(), loadJobs(), loadCollections(), loadItems()]);
  } catch (e) {
    problem(e.message);
  }
}

loadAll();
follow();
