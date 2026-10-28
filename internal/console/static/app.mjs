// SPDX-License-Identifier: AGPL-3.0-only

// The admin console. It calls the documented API on the same origin with the
// tenant key the operator pasted (kept in sessionStorage), exactly as curl
// would; in read-only mode it sends GET only and shows no action. Every value
// from the API reaches the page through textContent or an attribute set on a
// created element, never as markup. The pure helpers live in lib.mjs.

import {
  ApiError,
  HEALTH_RANGES,
  KEY_STORAGE,
  SLEEP_STAGES,
  STAGE_STATES,
  absTime,
  apiPath,
  attention,
  barPath,
  classifyConnection,
  classifyDelivery,
  classifyFreshness,
  classifyProvider,
  classifyRun,
  classifyUser,
  classifyWebhook,
  clip,
  compact,
  confirmMatches,
  dailySeries,
  dayLabel,
  formatAgo,
  formatCount,
  formatDuration,
  formatWindow,
  healthRange,
  humanize,
  initials,
  isCloud,
  keysPatch,
  labelDays,
  latest,
  linePath,
  makeApi,
  mean,
  niceTicks,
  parseRoute,
  pipelineStages,
  providerLabel,
  providerStatusLabel,
  recordZone,
  relTime,
  sleepNights,
  splitFigure,
  statSeries,
  syncSchedule,
  webhookLabel,
  workoutSeconds,
  workoutSummary,
  workoutTimeline,
} from "./lib.mjs";

// EXPORTS are the CSV exports of a user (GET /v1/users/{id}/exports/{dataset}),
// with their button labels.
const EXPORTS = [
  ["samples", "Samples"],
  ["category-samples", "Category samples"],
  ["sleep", "Sleep"],
  ["workouts", "Workouts"],
  ["nutrition", "Nutrition"],
  ["daily", "Daily summaries"],
  ["ecg", "ECG"],
];

// EVENT_TYPES are the webhook event types an endpoint may subscribe to.
const EVENT_TYPES = [
  "data.written",
  "data.deleted",
  "data.erased",
  "user.deleted",
  "connection.needs_reauth",
  "connection.broken",
  "connection.restored",
  "consent.granted",
  "consent.revoked",
  "tag.created",
  "tag.updated",
  "tag.deleted",
];

const KIND_LABELS = { oauth: "OAuth", push: "Push", device: "App upload" };
const STAGE_ICONS = { sources: "plug", sync: "refresh", processing: "cpu", storage: "database", delivery: "send" };
const SEVERITY_ICONS = { ok: "check-circle", warn: "alert", bad: "alert-circle" };

// ---------------------------------------------------------------------------
// Key storage

function storage() {
  try {
    return window.sessionStorage;
  } catch {
    return null;
  }
}

function getKey() {
  try {
    return storage()?.getItem(KEY_STORAGE) || "";
  } catch {
    return "";
  }
}

function setKey(v) {
  try {
    if (v) storage()?.setItem(KEY_STORAGE, v);
    else storage()?.removeItem(KEY_STORAGE);
  } catch {
    // Storage blocked: the key lives only until the next reload.
  }
}

// ---------------------------------------------------------------------------
// DOM building blocks

// el builds an element. Text children become text nodes; attrs are set as
// attributes (no event handler attributes: listeners are added in code).
function el(tag, attrs, ...children) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === null || v === undefined || v === false) continue;
    if (k === "class") e.className = v;
    else e.setAttribute(k, v === true ? "" : String(v));
  }
  for (const c of children.flat(Infinity)) {
    if (c === null || c === undefined || c === false) continue;
    e.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return e;
}

// icon draws one symbol of the page's sprite.
function icon(name) {
  const ns = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(ns, "svg");
  svg.setAttribute("class", "icon");
  svg.setAttribute("aria-hidden", "true");
  const use = document.createElementNS(ns, "use");
  use.setAttribute("href", `#i-${name}`);
  svg.append(use);
  return svg;
}

// svgNode builds an SVG element as el builds an HTML one.
function svgNode(tag, attrs, ...children) {
  const e = document.createElementNS("http://www.w3.org/2000/svg", tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === null || v === undefined || v === false) continue;
    e.setAttribute(k, v === true ? "" : String(v));
  }
  for (const c of children.flat(Infinity)) {
    if (c === null || c === undefined || c === false) continue;
    e.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return e;
}

// numeral draws a value as a display numeral, the figure large and its
// unit small beside it, as on the connect page.
function numeral(value, { href = null, cls = "" } = {}) {
  const f = splitFigure(value);
  const parts = [f.figure, f.unit ? el("span", { class: "unit" }, f.unit) : null];
  return el("div", { class: `numeral${f.word ? " word" : ""} ${cls}`.trim() }, href ? link(href, parts) : parts);
}

// scene is the frame's photographic background, drawn as on the connect
// page from gradients and the noise filters of the page's sprite.
function scene() {
  const ns = "http://www.w3.org/2000/svg";
  const svg = (cls, ...rects) => {
    const s = document.createElementNS(ns, "svg");
    s.setAttribute("class", cls);
    s.setAttribute("focusable", "false");
    for (const attrs of rects) {
      const r = document.createElementNS(ns, "rect");
      r.setAttribute("width", "100%");
      r.setAttribute("height", "100%");
      for (const [k, v] of Object.entries(attrs)) r.setAttribute(k, v);
      s.append(r);
    }
    return s;
  };
  return el(
    "div",
    { class: "scene", "aria-hidden": "true" },
    el("div", { class: "base" }),
    svg("foliage", { filter: "url(#scene-foliage)" }),
    el("div", { class: "mist" }),
    el("div", { class: "sheen p1" }),
    el("div", { class: "sheen p2" }),
    svg("dissolve", { fill: "url(#scene-fade)", filter: "url(#scene-specks-light)" }, { fill: "url(#scene-fade)", filter: "url(#scene-specks-dark)" }),
    svg("grain", { filter: "url(#scene-grain)" }),
    el("div", { class: "shade" }),
  );
}

function pill(cls, text) {
  return el("span", { class: `pill ${cls}` }, text ?? "-");
}

function tag(text, cls = "") {
  return el("span", { class: `tag ${cls}`.trim() }, text);
}

// when shows a time as "12 min ago", the exact UTC time on hover.
function when(iso) {
  if (!iso) return el("span", { class: "dim" }, "-");
  return el("time", { datetime: iso, title: absTime(iso) }, relTime(iso));
}

// mark is the initials tile of a provider, a user or a tenant, as on the
// connect page.
function mark(name, size = "") {
  return el("span", { class: `mark ${size}`.trim(), "aria-hidden": "true" }, initials(name));
}

function named(name) {
  return el("span", { class: "named" }, mark(name), el("strong", {}, name));
}

function cell2(main, sub) {
  return el("div", { class: "cell-2" }, el("span", {}, main), sub ? el("span", { class: "sub" }, sub) : null);
}

function num(n) {
  return el("td", { class: "num" }, n === null || n === undefined ? "-" : formatCount(n));
}

function link(href, ...text) {
  return el("a", { href }, text);
}

function goLink(href, text) {
  return el("a", { href, class: "go" }, text, icon("arrow"));
}

function notice(cls, ...children) {
  const ic = { ok: "check-circle", warn: "alert", bad: "alert-circle" }[cls] || "info";
  return el("div", { class: `notice ${cls}`.trim(), role: cls === "bad" ? "alert" : null }, icon(ic), el("div", {}, children));
}

function errorBox(err) {
  return notice("bad", err instanceof ApiError && err.detail ? err.detail : err instanceof Error ? err.message : String(err));
}

// empty is what a list shows with nothing in it. good marks an empty list as
// the healthy state (no parked job, nothing failing).
function empty(title, text, { good = false } = {}) {
  return el(
    "div",
    { class: `empty${good ? " good" : ""}` },
    el("span", { class: "empty-icon" }, icon(good ? "check" : "info")),
    el("div", { class: "empty-title" }, title),
    text ? el("p", {}, text) : null,
  );
}

// table builds a table; a header is a string or [text, "num"]. With no row,
// it shows the empty node instead.
function table(headers, rows, emptyNode) {
  if (!rows.length) return emptyNode;
  const th = headers.map((h) => (Array.isArray(h) ? el("th", { class: h[1] }, h[0]) : el("th", {}, h)));
  return el("div", { class: "scroll" }, el("table", {}, el("thead", {}, el("tr", {}, th)), el("tbody", {}, rows)));
}

function facts(pairs) {
  return el(
    "dl",
    { class: "facts" },
    pairs.filter(Boolean).map(([k, v]) => [el("dt", {}, k), el("dd", {}, v)]),
  );
}

// card is a titled panel. Its children are card bodies, tables or footers.
function card({ title, count = null, hint = null, actions = null, cls = "", lead = null }, ...children) {
  const acts = [actions].flat().filter(Boolean);
  return el(
    "section",
    { class: `card ${cls}`.trim() },
    title
      ? el(
          "div",
          { class: "card-head" },
          el("div", { class: "title" }, lead, el("h2", {}, title), count !== null ? el("span", { class: "count" }, formatCount(count)) : null),
          acts.length ? el("div", { class: "actions" }, acts) : null,
          hint ? el("p", { class: "hint" }, hint) : null,
        )
      : null,
    children,
  );
}

function body(...children) {
  return el("div", { class: "card-body" }, children);
}

function foot(...children) {
  return el("div", { class: "card-foot" }, children);
}

function stats(items) {
  return el(
    "div",
    { class: "stats" },
    items.map((s) => el("div", { class: "stat" }, el("div", { class: "label" }, s.label), numeral(s.value, { cls: `stat-value ${s.cls || ""}` }))),
  );
}

function crumbsNav(crumbs) {
  return el(
    "nav",
    { class: "crumbs", "aria-label": "Breadcrumb" },
    crumbs.map(([text, href], i) => [i > 0 ? icon("chevron") : null, href ? link(href, text) : el("span", {}, text)]),
  );
}

// pageHead is the top of a page: crumbs or the section's label, the title,
// a line under it and actions.
function pageHead({ title, sub = null, actions = null, crumbs = null, eyebrow = null, lead = null, status = null }) {
  const acts = [actions].flat().filter(Boolean);
  return [
    crumbs ? crumbsNav(crumbs) : eyebrow ? el("div", { class: "label eyebrow" }, eyebrow) : null,
    el(
      "div",
      { class: "page-head" },
      el(
        "div",
        { class: "page-title-wrap" },
        el("div", { class: "page-title" }, lead, el("h1", {}, title), status),
        sub ? el("p", { class: "sub" }, sub) : null,
      ),
      acts.length ? el("div", { class: "actions" }, acts) : null,
    ),
  ];
}

function kindsText(kinds) {
  return (kinds || []).map((k) => KIND_LABELS[k] || k).join(" · ") || "-";
}

function healthCell(h) {
  if (!h || h.healthy) return pill("ok", "Healthy");
  const action = { reconnect: "The user reconnects", wait: "Resumes on its own" }[h.action] || humanize(h.action);
  return cell2(pill(h.action === "reconnect" ? "bad" : "warn", humanize(h.reason || "unhealthy")), action);
}

// ---------------------------------------------------------------------------
// Charts: inline SVG, one value per day of a range

// CHART_PAD is the room around a plot: its tick labels on the left, the
// dates below.
const CHART_PAD = { top: 10, right: 6, bottom: 24, left: 42 };

// band is where the bar of day i stands: at most 24 px wide, a gap of
// surface between neighbours once there is room for one.
function band(geo, i) {
  const gap = geo.step >= 8 ? 2 : geo.step >= 4 ? 1 : 0;
  const w = Math.max(1, Math.min(24, geo.step - gap));
  return { x: geo.x(i) - w / 2, w };
}

// dayChart draws a chart of the days of a range, sized to its box and drawn
// again when the box changes width. ticks are the y axis (niceTicks; null
// for none) and tick renders one; left is the room for the labels left of
// the plot, or a function of the width giving it. draw(g, geo) adds the marks to g and
// returns those of each day; tip(i) gives the readout of day i, rows of
// [series class, value, label], or null when the day holds nothing. A
// pointer, or the arrow keys once the plot has focus, moves a crosshair over
// the days and the readout follows it. The table view under each chart
// holds the same values for whoever cannot use either.
function dayChart({ days, label, ticks = null, tick = String, height = 150, left: leftOf = CHART_PAD.left, draw, tip }) {
  const box = el("div", { class: "chart-plot", tabindex: "0", role: "img", "aria-label": label });
  const readout = el("div", { class: "chart-tip", "aria-hidden": "true", hidden: true });
  let geo = null;
  let marks = [];
  let guide = null;
  let active = -1;
  let drawn = 0;

  const render = (width) => {
    const w = Math.round(width);
    const left = typeof leftOf === "function" ? leftOf(w) : leftOf;
    const h = height + CHART_PAD.top + CHART_PAD.bottom;
    const plotW = w - left - CHART_PAD.right;
    const step = plotW / days.length;
    const y = ticks ? (v) => CHART_PAD.top + height * (1 - (v - ticks.lo) / (ticks.hi - ticks.lo)) : null;
    geo = { width: w, left, top: CHART_PAD.top, height, plotW, step, x: (i) => left + (i + 0.5) * step, y };
    const svg = svgNode("svg", { width: w, height: h, viewBox: `0 0 ${w} ${h}`, "aria-hidden": "true", focusable: "false" });
    for (const t of ticks ? ticks.ticks : []) {
      const ty = Math.round(y(t)) + 0.5;
      svg.append(
        svgNode("line", { class: t === ticks.lo ? "grid base" : "grid", x1: left, x2: w - CHART_PAD.right, y1: ty, y2: ty }),
        svgNode("text", { class: "tick", x: left - 8, y: ty, "text-anchor": "end", "dominant-baseline": "middle" }, tick(t)),
      );
    }
    for (const i of labelDays(days.length, Math.floor(plotW / 70))) {
      const x = geo.x(i);
      const end = w - x < 26;
      svg.append(svgNode("text", { class: "tick", x: end ? w - CHART_PAD.right : x, y: h - 6, "text-anchor": end ? "end" : "middle" }, dayLabel(days[i])));
    }
    guide = svgNode("line", { class: "guide", y1: CHART_PAD.top, y2: CHART_PAD.top + height, hidden: true });
    const g = svgNode("g", {});
    svg.append(guide, g);
    marks = draw(g, geo);
    active = -1;
    return svg;
  };

  const hide = () => {
    for (const m of marks[active] || []) m.classList.remove("on");
    active = -1;
    box.classList.remove("hovering");
    guide?.setAttribute("hidden", "");
    readout.hidden = true;
  };

  const show = (i) => {
    if (!geo) return;
    const at = Math.max(0, Math.min(days.length - 1, i));
    if (at === active) return;
    for (const m of marks[active] || []) m.classList.remove("on");
    active = at;
    for (const m of marks[at] || []) m.classList.add("on");
    // Only a day with marks dims the others.
    box.classList.toggle("hovering", Boolean(marks[at]?.length));
    const x = Math.round(geo.x(at)) + 0.5;
    guide.setAttribute("x1", x);
    guide.setAttribute("x2", x);
    guide.removeAttribute("hidden");
    const rows = tip(at);
    readout.replaceChildren(
      el("div", { class: "tip-date" }, dayLabel(days[at], true)),
      ...(rows
        ? rows.map(([cls, value, name]) => el("div", { class: "tip-row" }, cls ? el("i", { class: `key ${cls}` }) : null, el("strong", {}, value), name ? el("span", {}, name) : null))
        : [el("div", { class: "tip-row" }, el("span", {}, "No data"))]),
    );
    readout.hidden = false;
    // Beside the crosshair, on the side with room, so it hides no mark of
    // the day.
    const tw = readout.offsetWidth;
    readout.style.left = `${x + 12 + tw <= geo.width ? x + 12 : Math.max(0, x - 12 - tw)}px`;
  };

  const redraw = () => {
    const w = box.clientWidth;
    if (!w || w === drawn) return;
    drawn = w;
    hide();
    box.replaceChildren(render(w), readout);
  };
  const pointer = (ev) => {
    if (!geo) return;
    show(Math.floor((ev.clientX - box.getBoundingClientRect().left - geo.left) / geo.step));
  };
  box.addEventListener("pointermove", pointer);
  box.addEventListener("pointerdown", pointer);
  box.addEventListener("pointerleave", hide);
  box.addEventListener("blur", hide);
  box.addEventListener("keydown", (ev) => {
    const to = { ArrowLeft: active - 1, ArrowRight: active + 1, Home: 0, End: days.length - 1 }[ev.key];
    if (ev.key === "Escape") hide();
    if (to === undefined) return;
    ev.preventDefault();
    show(active < 0 ? days.length - 1 : to);
  });
  if (typeof ResizeObserver === "function") new ResizeObserver(redraw).observe(box);
  else window.requestAnimationFrame(redraw);
  return box;
}

// barChart draws one value per day as bars from zero.
function barChart({ days, values, cls, name, fmt, tick = compact, label }) {
  const ticks = niceTicks(0, Math.max(0, ...values.map((v) => v || 0)));
  return dayChart({
    days,
    label,
    ticks,
    tick,
    draw: (g, geo) =>
      values.map((v, i) => {
        if (!v || v <= 0) return [];
        const b = band(geo, i);
        const top = geo.y(v);
        const bar = svgNode("path", { class: `mark ${cls}`, d: barPath(b.x, top, b.w, geo.y(ticks.lo) - top) });
        g.append(bar);
        return [bar];
      }),
    tip: (i) => (values[i] === null ? null : [[cls, fmt(values[i]), name]]),
  });
}

// lineChart draws one value per day as a line through the days that have
// one. A dot marks the latest; sparse series, where the line alone would
// hide how few days hold a value, get a dot on every point.
function lineChart({ days, values, cls, name, fmt, tick = String, label }) {
  const have = values.filter((v) => v !== null);
  const ticks = niceTicks(Math.min(...have), Math.max(...have));
  return dayChart({
    days,
    label,
    ticks,
    tick,
    draw: (g, geo) => {
      const points = values.map((v, i) => (v === null ? null : [geo.x(i), geo.y(v)]));
      const last = latest(values);
      const sparse = geo.step >= 10 && have.length < days.length * 0.6;
      g.append(svgNode("path", { class: `line ${cls}`, d: linePath(points.filter(Boolean)) }));
      return points.map((p, i) => {
        if (!p) return [];
        const shown = sparse || have.length === 1 || i === last.index;
        const dot = svgNode("circle", { class: `dot ${cls}${shown ? "" : " hover-dot"}`, cx: p[0], cy: p[1], r: 4 });
        g.append(dot);
        return [dot];
      });
    },
    tip: (i) => (values[i] === null ? null : [[cls, fmt(values[i]), name]]),
  });
}

// sleepChart stacks the stages of each night, deep at the baseline, a gap
// of surface between them; only the top of a night is rounded.
function sleepChart({ days, nights, label }) {
  const ticks = niceTicks(0, Math.max(...nights.map((n) => (n ? n.total / 3600 : 0))));
  return dayChart({
    days,
    label,
    ticks,
    tick: (t) => `${t} h`,
    height: 170,
    draw: (g, geo) =>
      nights.map((n, i) => {
        if (!n) return [];
        const b = band(geo, i);
        const parts = SLEEP_STAGES.filter(([k]) => n[k] > 0);
        const out = [];
        let acc = 0;
        parts.forEach(([k], j) => {
          const bottom = geo.y(acc / 3600) - (j > 0 ? 1 : 0);
          acc += n[k];
          const top = geo.y(acc / 3600) + (j < parts.length - 1 ? 1 : 0);
          if (bottom - top <= 0) return;
          const seg = svgNode("path", { class: `mark s-${k}`, d: barPath(b.x, top, b.w, bottom - top, j === parts.length - 1 ? 4 : 0) });
          g.append(seg);
          out.push(seg);
        });
        return out;
      }),
    tip: (i) => {
      const n = nights[i];
      if (!n) return null;
      return [
        [null, formatDuration(n.total), "asleep"],
        ...SLEEP_STAGES.filter(([k]) => n[k] > 0).map(([k, l]) => [`s-${k}`, formatDuration(n[k]), l]),
        n.awake > 0 ? [null, formatDuration(n.awake), "awake"] : null,
      ].filter(Boolean);
    },
  });
}

// workoutChart is the timeline of the workouts: a lane per activity type,
// named on the left, a dot per workout on the day it started.
function workoutChart({ days, timeline, label }) {
  const lane = 28;
  return dayChart({
    days,
    label,
    height: timeline.rows.length * lane,
    left: (w) => (w < 480 ? 96 : 132),
    draw: (g, geo) => {
      timeline.rows.forEach((r, j) => {
        const cy = geo.top + j * lane + lane / 2;
        g.append(
          svgNode("line", { class: "grid", x1: geo.left, x2: geo.left + geo.plotW, y1: Math.round(cy) + 0.5, y2: Math.round(cy) + 0.5 }),
          svgNode("text", { class: "lane", x: 0, y: cy, "dominant-baseline": "middle" }, clip(r.label, geo.left < 120 ? 12 : 18)),
        );
      });
      const out = days.map(() => []);
      for (const m of timeline.marks) {
        const dot = svgNode("circle", { class: "mark dot s-activity", cx: geo.x(m.day), cy: geo.top + m.row * lane + lane / 2, r: 5 });
        g.append(dot);
        out[m.day].push(dot);
      }
      return out;
    },
    tip: (i) => {
      const day = timeline.marks.filter((m) => m.day === i);
      return day.length ? day.map((m) => ["s-activity", humanize(m.workout.activity_type), workoutSummary(m.workout)]) : null;
    },
  });
}

// chartPanel frames one chart: its title and headline figure, the plot (an
// empty state when the range holds nothing, the error when the read
// failed), its legend and its table view.
function chartPanel({ title, figure = null, unit = null, plot = null, error = null, emptyText = "", legend = null, rows = null, headers = null }) {
  return el(
    "figure",
    { class: "chart" },
    el(
      "figcaption",
      { class: "chart-head" },
      el("h3", { class: "chart-title" }, title),
      figure !== null ? el("div", { class: "chart-figure" }, figure, unit ? el("span", { class: "unit" }, unit) : null) : null,
    ),
    error ? errorBox(error) : plot || empty(emptyText),
    plot && legend && legend.length > 1
      ? el(
          "div",
          { class: "legend" },
          legend.map(([cls, text]) => el("span", {}, el("i", { class: `swatch ${cls}` }), text)),
        )
      : null,
    plot && rows ? el("details", { class: "chart-table" }, el("summary", {}, "Table"), table(headers, rows, null)) : null,
  );
}

// healthCharts lays out the charts of one read of the user's health. Sleep,
// heart, steps and workouts always show, empty when the range has none;
// active energy, weight and body fat only when the user has some.
function healthCharts({ n, days, zone, daily, sleep, workouts, rhr, hrv, hrvKind, weight, fat }) {
  const span = `the last ${n} days`;
  const plotLabel = (title, figure, unit, count) =>
    `${title}, ${span}: ${figure} ${unit}, ${count} of ${n} days with data. Arrow keys read each day.`;
  const dated = (values, fmt) =>
    values
      .map((v, i) => [days[i], v])
      .filter(([, v]) => v !== null)
      .reverse()
      .map(([d, v]) => el("tr", {}, el("td", { class: "nowrap" }, dayLabel(d, true)), el("td", { class: "num" }, fmt(v))));

  // Sleep
  const nights = sleep.ok ? sleepNights(sleep.value.data, days, zone) : [];
  const slept = nights.filter(Boolean);
  const sleepFigure = slept.length ? formatDuration(mean(slept.map((x) => x.total))) : null;
  const stages = SLEEP_STAGES.filter(([k]) => slept.some((x) => x[k] > 0));
  const sleepPanel = chartPanel({
    title: "Sleep",
    figure: sleepFigure,
    unit: "a night on average",
    error: sleep.ok ? null : sleep.error,
    emptyText: `No sleep in ${span}`,
    plot: slept.length ? sleepChart({ days, nights, label: plotLabel("Sleep", sleepFigure, "a night on average", slept.length) }) : null,
    legend: stages.map(([k, l]) => [`s-${k}`, l]),
    headers: ["Woke up", ["Asleep", "num"], ...stages.map(([, l]) => [l, "num"]), ["Awake", "num"]],
    rows: slept
      .slice()
      .reverse()
      .map((x) =>
        el(
          "tr",
          {},
          el("td", { class: "nowrap" }, dayLabel(x.date, true)),
          el("td", { class: "num" }, formatDuration(x.total)),
          stages.map(([k]) => el("td", { class: "num" }, x[k] ? formatDuration(x[k]) : "-")),
          el("td", { class: "num" }, x.awake ? formatDuration(x.awake) : "-"),
        ),
      ),
  });

  // series is the panel of one value per day, as bars or a line.
  const series = ({ title, result, values, kind, cls, name, fmt, figure, unit, tick, emptyText, header }) =>
    chartPanel({
      title,
      figure: values.some((v) => v !== null) ? figure : null,
      unit,
      error: result.ok ? null : result.error,
      emptyText,
      plot: values.some((v) => v !== null)
        ? (kind === "bar" ? barChart : lineChart)({ days, values, cls, name, fmt, tick, label: plotLabel(title, figure, unit, values.filter((v) => v !== null).length) })
        : null,
      headers: ["Date", [header, "num"]],
      rows: dated(values, fmt),
    });

  const rhrValues = rhr.ok ? statSeries(rhr.value, days) : [];
  const rhrPanel = series({
    title: "Resting heart rate",
    result: rhr,
    values: rhrValues,
    kind: "line",
    cls: "s-heart",
    name: "resting",
    fmt: (v) => `${Math.round(v)} bpm`,
    figure: String(Math.round(mean(rhrValues))),
    unit: "bpm on average",
    emptyText: `No resting heart rate in ${span}`,
    header: "bpm",
  });
  const hrvValues = hrv.ok ? statSeries(hrv.value, days) : [];
  const hrvPanel = series({
    title: `Heart rate variability${hrvKind ? ` (${hrvKind})` : ""}`,
    result: hrv,
    values: hrvValues,
    kind: "line",
    cls: "s-heart",
    name: hrvKind,
    fmt: (v) => `${Math.round(v)} ms`,
    figure: String(Math.round(mean(hrvValues))),
    unit: "ms on average",
    emptyText: `No heart rate variability in ${span}`,
    header: "ms",
  });

  const stepValues = daily.ok ? dailySeries(daily.value.data, days, "step_count") : [];
  const stepPanel = series({
    title: "Steps",
    result: daily,
    values: stepValues,
    kind: "bar",
    cls: "s-activity",
    name: "steps",
    fmt: (v) => formatCount(v),
    figure: formatCount(mean(stepValues)),
    unit: "a day on average",
    emptyText: `No steps in ${span}`,
    header: "Steps",
  });
  const kcalValues = daily.ok ? dailySeries(daily.value.data, days, "active_kcal") : [];
  const kcalPanel = kcalValues.some((v) => v !== null)
    ? series({
        title: "Active energy",
        result: daily,
        values: kcalValues,
        kind: "bar",
        cls: "s-activity",
        name: "active",
        fmt: (v) => `${formatCount(v)} kcal`,
        figure: formatCount(mean(kcalValues)),
        unit: "kcal a day on average",
        header: "kcal",
      })
    : null;

  const timeline = workouts.ok ? workoutTimeline(workouts.value.data, days, zone) : { rows: [], marks: [], seconds: 0 };
  const count = timeline.marks.length;
  const workoutUnit = `${count === 1 ? "workout" : "workouts"}, ${formatDuration(timeline.seconds)} in all`;
  const workoutPanel = chartPanel({
    title: "Workouts",
    figure: count ? formatCount(count) : null,
    unit: workoutUnit,
    error: workouts.ok ? null : workouts.error,
    emptyText: `No workout in ${span}`,
    plot: count ? workoutChart({ days, timeline, label: plotLabel("Workouts", formatCount(count), workoutUnit, new Set(timeline.marks.map((m) => m.day)).size) }) : null,
    headers: ["Date", "Activity", ["Duration", "num"], "Details"],
    rows: timeline.marks
      .slice()
      .reverse()
      .map((m) =>
        el(
          "tr",
          {},
          el("td", { class: "nowrap" }, dayLabel(days[m.day], true)),
          el("td", {}, humanize(m.workout.activity_type)),
          el("td", { class: "num" }, formatDuration(workoutSeconds(m.workout))),
          el("td", { class: "dim" }, workoutSummary({ ...m.workout, duration_s: 0 }) || "-"),
        ),
      ),
  });

  // measure is the panel of a body measurement taken now and then: the
  // latest value leads, with how far it moved over the range.
  const measure = (title, result, unit, digits) => {
    const values = result.ok ? statSeries(result.value, days) : [];
    if (result.ok && !values.some((v) => v !== null)) return null;
    const last = latest(values);
    const first = values.find((v) => v !== null);
    const moved = last ? last.value - first : 0;
    const change = last && last.index > values.indexOf(first) ? `, ${moved >= 0 ? "+" : "−"}${Math.abs(moved).toFixed(digits)} since ${dayLabel(days[values.indexOf(first)])}` : "";
    return series({
      title,
      result,
      values,
      kind: "line",
      cls: "s-body",
      name: title.toLowerCase(),
      fmt: (v) => `${v.toFixed(digits)} ${unit}`,
      figure: last ? last.value.toFixed(digits) : "-",
      unit: `${unit} latest${change}`,
      header: unit,
    });
  };
  const weightPanel = measure("Weight", weight, "kg", 1);
  const fatPanel = measure("Body fat", fat, "%", 1);

  // Two charts a row; one alone in its row takes the whole width.
  const rowsOf = [[sleepPanel], [rhrPanel, hrvPanel], [stepPanel, kcalPanel], [workoutPanel], [weightPanel, fatPanel]];
  return el(
    "div",
    { class: "charts" },
    rowsOf.flatMap((row) => {
      const kept = row.filter(Boolean);
      if (kept.length === 1) kept[0].classList.add("wide");
      return kept;
    }),
  );
}

function main() {
  const meta = document.querySelector('meta[name="ohs-console-mode"]');
  const readOnly = !meta || meta.getAttribute("content") !== "manage";
  const api = makeApi((u, o) => window.fetch(u, o), getKey, { readOnly });
  const view = document.getElementById("view");
  const signin = document.getElementById("signin");
  const sidebar = document.getElementById("sidebar");
  const nav = document.getElementById("nav");
  const signout = document.getElementById("signout");
  const tenantLabel = document.getElementById("tenant");
  const tenantRole = document.getElementById("tenant-role");
  const tenantMark = document.getElementById("tenant-mark");
  const modeLabel = document.getElementById("mode");
  const menu = document.getElementById("menu");
  const form = document.getElementById("signin-form");
  const keyInput = document.getElementById("key");
  const signinError = document.getElementById("signin-error");
  const setupLink = document.getElementById("setup-link");
  const flashBox = document.getElementById("flash");
  const dialog = document.getElementById("dialog");
  let renderSeq = 0;
  let tenant = null;
  // names maps a provider slug to its display name, as the server gave it.
  let names = {};
  // Cursor stacks of the paged views: page n starts at cursors[n].
  const cursors = { users: [null], audit: [null] };
  let auditAction = "";

  modeLabel.hidden = !readOnly;
  const pname = (slug) => providerLabel(names, slug);

  // -------------------------------------------------------------------------
  // Feedback

  // flash shows a toast. Success and warnings go by themselves; an error
  // stays a little longer and can be closed.
  function flash(message, cls = "ok") {
    const close = el("button", { type: "button", "aria-label": "Dismiss" }, icon("x"));
    const toast = el("div", { class: `toast ${cls}` }, icon(SEVERITY_ICONS[cls] || "info"), el("div", { class: "toast-text" }, message), close);
    const drop = () => toast.remove();
    close.addEventListener("click", drop);
    flashBox.append(toast);
    while (flashBox.children.length > 3) flashBox.firstChild.remove();
    setTimeout(drop, cls === "bad" ? 12000 : 5000);
  }

  function failure(err) {
    if (err instanceof ApiError && err.status === 401) {
      setKey("");
      showSignin("The key is no longer accepted. Sign in again.");
      return;
    }
    flash(err instanceof ApiError && err.detail ? err.detail : err.message || String(err), "bad");
  }

  // button builds an action button. In read-only mode it is not built. The
  // click runs fn with the button disabled; an error shows as a toast.
  function button(text, fn, { kind = "", title = null, icon: ic = null } = {}) {
    if (readOnly) return null;
    const b = el("button", { type: "button", class: kind || null, title }, ic ? icon(ic) : null, text);
    b.addEventListener("click", async (ev) => {
      ev.stopPropagation();
      b.disabled = true;
      try {
        await fn();
      } catch (err) {
        failure(err);
      } finally {
        b.disabled = false;
      }
    });
    return b;
  }

  // ask shows a modal dialog. typed asks the operator to type that text
  // first; extra are more form fields. run, when given, does the work while
  // the dialog stays open: an error shows in the dialog, and ask resolves
  // with what run returned. Without run, ask resolves true when confirmed.
  // Cancel, Escape or a click outside resolve false.
  function ask({ title, body: text = [], confirm = "Confirm", danger = false, typed = null, extra = null, run = null, wide = false, focus = null }) {
    return new Promise((resolve) => {
      const input = typed
        ? el("input", { type: "text", class: "mono", autocomplete: "off", spellcheck: "false", "aria-label": `Type ${typed} to confirm` })
        : null;
      const ok = el("button", { type: "submit", class: danger ? "danger-solid" : "primary", disabled: Boolean(typed) }, confirm);
      const cancel = el("button", { type: "button" }, "Cancel");
      const error = el("p", { class: "form-error", role: "alert" });
      const box = el(
        "form",
        { method: "dialog", class: "dialog-body" },
        el(
          "div",
          { class: "dialog-main" },
          el("h2", {}, title),
          [text].flat().map((p) => (p instanceof Node ? p : el("p", {}, p))),
          extra,
          typed ? el("div", { class: "form-field" }, el("label", {}, "Type ", el("code", {}, typed), " to confirm"), input) : null,
          error,
        ),
        el("div", { class: "dialog-foot" }, cancel, ok),
      );
      if (input) input.addEventListener("input", () => (ok.disabled = !confirmMatches(input.value, typed)));
      let answered = false;
      const done = (v) => {
        if (answered) return;
        answered = true;
        dialog.close();
        resolve(v);
      };
      box.addEventListener("submit", async (ev) => {
        ev.preventDefault();
        if (typed && !confirmMatches(input.value, typed)) return;
        if (!run) {
          done(true);
          return;
        }
        ok.disabled = true;
        cancel.disabled = true;
        error.textContent = "";
        try {
          done((await run()) ?? true);
        } catch (err) {
          if (err instanceof ApiError && err.status === 401) {
            done(false);
            failure(err);
            return;
          }
          error.textContent = err instanceof ApiError && err.detail ? err.detail : err.message || String(err);
          ok.disabled = false;
          cancel.disabled = false;
        }
      });
      cancel.addEventListener("click", () => done(false));
      dialog.onclose = () => done(false);
      dialog.onclick = (ev) => {
        if (ev.target === dialog && !cancel.disabled) done(false);
      };
      dialog.className = wide ? "wide" : "";
      dialog.replaceChildren(box);
      dialog.showModal();
      (focus || input || ok).focus();
    });
  }

  // reveal shows a value the API returns once (a key, a signing secret, a
  // link) with a copy button. Dismissed, it is gone from the page.
  function reveal({ title, intro, value, note }) {
    return new Promise((resolve) => {
      const code = el("code", { class: "secret" }, value);
      const copy = el("button", { type: "button" }, icon("copy"), "Copy");
      copy.addEventListener("click", async () => {
        try {
          await navigator.clipboard.writeText(value);
          copy.replaceChildren(icon("check"), "Copied");
        } catch {
          const range = document.createRange();
          range.selectNodeContents(code);
          const sel = window.getSelection();
          sel.removeAllRanges();
          sel.addRange(range);
          copy.textContent = "Selected: press Ctrl+C";
        }
      });
      const close = el("button", { type: "submit", class: "primary" }, "Done");
      const box = el(
        "form",
        { method: "dialog", class: "dialog-body" },
        el(
          "div",
          { class: "dialog-main" },
          el("h2", {}, title),
          el("p", {}, intro),
          el("div", { class: "secret-line" }, code, copy),
          note ? notice("warn", note) : null,
        ),
        el("div", { class: "dialog-foot" }, close),
      );
      box.addEventListener("submit", (ev) => {
        ev.preventDefault();
        dialog.close();
      });
      dialog.onclose = () => {
        dialog.replaceChildren();
        resolve();
      };
      dialog.onclick = null;
      dialog.className = "wide";
      dialog.replaceChildren(box);
      dialog.showModal();
      copy.focus();
    });
  }

  function copyField(value) {
    const b = el("button", { type: "button", class: "small", "aria-label": "Copy" }, icon("copy"), "Copy");
    b.addEventListener("click", async () => {
      try {
        await navigator.clipboard.writeText(value);
        b.replaceChildren(icon("check"), "Copied");
        setTimeout(() => b.replaceChildren(icon("copy"), "Copy"), 2000);
      } catch {
        b.textContent = "Copy failed";
      }
    });
    return el("div", { class: "copy-field" }, el("code", { title: value }, value), b);
  }

  // download fetches a file with the key in the header, never in a URL, and
  // hands it to the browser.
  async function download(path, fallbackName) {
    const { blob, filename } = await api(path, { download: true });
    const url = URL.createObjectURL(blob);
    const a = el("a", { href: url, download: filename === "download" ? fallbackName : filename });
    document.body.append(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }

  // clickable makes a whole row open href, keeping its own links and
  // buttons working.
  function clickable(tr, href) {
    tr.classList.add("clickable");
    tr.addEventListener("click", (ev) => {
      if (ev.target.closest("a, button, input, label, select")) return;
      if (String(window.getSelection?.() ?? "")) return;
      go(href);
    });
    return tr;
  }

  function pager(stack, nextCursor, label) {
    const page = Math.max(0, stack.length - 1);
    const prev = el("button", { type: "button", class: "small", disabled: page === 0 }, label[0]);
    const next = el("button", { type: "button", class: "small", disabled: !nextCursor }, label[1]);
    prev.addEventListener("click", () => {
      if (stack.length > 1) stack.pop();
      render();
    });
    next.addEventListener("click", () => {
      if (nextCursor) stack.push(nextCursor);
      render();
    });
    return el("div", { class: "pager" }, el("span", {}, `Page ${page + 1}`), el("div", { class: "actions" }, prev, next));
  }

  // -------------------------------------------------------------------------
  // Sign in

  function showSignin(message) {
    tenant = null;
    signin.hidden = false;
    sidebar.hidden = true;
    setMenu(false);
    view.hidden = true;
    view.replaceChildren();
    signinError.textContent = message || "";
    keyInput.focus();
    checkSetup();
  }

  // checkSetup shows the first-run link while the instance has no tenant.
  // A 404 (OHS_SETUP_TOKEN not set) or any failure leaves it hidden.
  async function checkSetup() {
    try {
      const b = await api("/v1/setup", { auth: false });
      setupLink.hidden = !(b && b.needed === true);
    } catch {
      setupLink.hidden = true;
    }
  }

  function signOut() {
    setKey("");
    cursors.users = [null];
    cursors.audit = [null];
    names = {};
    keyInput.value = "";
    showSignin("");
  }

  async function loadTenant() {
    tenant = await api("/v1/tenant");
    tenantLabel.textContent = tenant.name;
    tenantRole.textContent = tenant.admin ? "Instance admin" : "Tenant";
    tenantMark.className = "mark ink";
    tenantMark.textContent = initials(tenant.name);
    // Provider names: every provider of the build for the admin, the ones
    // on for any other tenant. A provider missing here is named by its slug.
    const list = await settle(tenant.admin ? "/v1/admin/providers" : "/v1/providers");
    names = {};
    if (list.ok) for (const p of list.value.data || []) if (p.name) names[p.slug] = p.name;
  }

  form.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const key = keyInput.value.trim();
    keyInput.value = "";
    if (!key) return;
    setKey(key);
    try {
      await loadTenant();
    } catch (err) {
      setKey("");
      showSignin(
        err instanceof ApiError && (err.status === 401 || err.status === 403)
          ? "This key was refused. A tenant API key is needed, not a user token."
          : `Sign in failed: ${err.message}`,
      );
      return;
    }
    render();
  });
  signout.addEventListener("click", signOut);
  window.addEventListener("hashchange", () => render());

  // On a phone the sidebar is a bar with a menu button. The menu closes on
  // a link, on the dimmed page around it and on Escape.
  function setMenu(open) {
    sidebar.classList.toggle("open", open);
    menu.setAttribute("aria-expanded", String(open));
    menu.setAttribute("aria-label", open ? "Close the menu" : "Open the menu");
  }
  menu.addEventListener("click", () => setMenu(!sidebar.classList.contains("open")));
  sidebar.addEventListener("click", (ev) => {
    if (ev.target === sidebar || ev.target.closest("a")) setMenu(false);
  });
  document.addEventListener("keydown", (ev) => {
    if (ev.key !== "Escape" || !sidebar.classList.contains("open")) return;
    setMenu(false);
    menu.focus();
  });

  function go(hash) {
    if (window.location.hash === hash) render();
    else window.location.hash = hash;
  }

  async function render() {
    if (!getKey()) {
      showSignin("");
      return;
    }
    signin.hidden = true;
    sidebar.hidden = false;
    view.hidden = false;
    const seq = ++renderSeq;
    const route = parseRoute(window.location.hash);
    for (const a of nav.querySelectorAll("a")) {
      const on = parseRoute(a.getAttribute("href")).section === route.section;
      a.classList.toggle("active", on);
      if (on) a.setAttribute("aria-current", "page");
      else a.removeAttribute("aria-current");
    }
    if (!view.firstChild) view.replaceChildren(el("p", { class: "view-loading" }, "Loading..."));
    view.setAttribute("aria-busy", "true");
    let content;
    try {
      if (!tenant) await loadTenant();
      const views = {
        overview: overviewView,
        providers: providersView,
        provider: providerView,
        users: usersView,
        user: userView,
        jobs: jobsView,
        webhooks: webhooksView,
        webhook: webhookView,
        keys: keysView,
        audit: auditView,
      };
      content = await views[route.view](route.id);
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        setKey("");
        showSignin("The key is no longer accepted. Sign in again.");
        return;
      }
      content = errorBox(err);
    }
    if (seq !== renderSeq) return; // a later navigation won
    view.removeAttribute("aria-busy");
    view.replaceChildren(content);
  }

  // settle runs a GET and returns {ok, value} or {ok: false, error}, so one
  // failing call does not blank a whole view.
  async function settle(path, opts) {
    try {
      return { ok: true, value: await api(path, opts) };
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) throw error;
      return { ok: false, error };
    }
  }

  // -------------------------------------------------------------------------
  // Overview

  async function overviewView() {
    const [pipeline, healthz, readyz] = await Promise.all([
      settle("/v1/pipeline"),
      settle("/healthz", { auth: false }),
      settle("/readyz", { auth: false }),
    ]);
    const refresh = el("button", { type: "button", class: "icon-only", "aria-label": "Refresh", title: "Refresh" }, icon("refresh"));
    refresh.addEventListener("click", () => render());
    const probe = (r, okText, badText) => {
      const p = pill(r.ok ? "ok" : "bad", r.ok ? okText : badText);
      if (!r.ok) p.title = r.error.message || "";
      return p;
    };
    const head = (sub) =>
      pageHead({
        eyebrow: "Pipeline",
        title: "Overview",
        sub,
        actions: [probe(healthz, "API live", "API down"), probe(readyz, "Ready", "Not ready"), refresh],
      });
    if (!pipeline.ok) return el("div", {}, head(null), errorBox(pipeline.error));
    const p = pipeline.value;

    // The pipeline is the overview's frame: one glass panel per stage, in
    // the order data flows.
    const stages = pipelineStages(p);
    const flow = el(
      "section",
      { class: "frame band", "aria-label": "Data flow" },
      scene(),
      el("div", { class: "frame-top" }, el("span", { class: "label" }, "Data flow"), el("span", { class: "label" }, "Updated ", when(p.generated_at))),
      el(
        "div",
        { class: "pipeline", role: "list", "aria-label": "Pipeline, in the order data flows" },
        stages.map((s, i) => {
          const [big, ...rest] = s.figures;
          return [
            i > 0 ? el("span", { class: "flow-link", "aria-hidden": "true" }, icon("chevron")) : null,
            el(
              "div",
              { class: `glass stage state-${s.cls}`, role: "listitem" },
              el("div", { class: "stage-top" }, el("span", { class: "stage-name" }, icon(STAGE_ICONS[s.key] || "layers"), s.title), pill(s.cls, STAGE_STATES[s.cls])),
              el("div", { class: "stage-role" }, s.role),
              numeral(big.value, { href: big.href }),
              el("div", { class: "label stage-big-label" }, big.label),
              el(
                "dl",
                {},
                rest.map((f) => [el("dt", {}, f.label), el("dd", { class: f.cls || null }, f.href ? link(f.href, f.value) : f.value)]),
              ),
            ),
          ];
        }),
      ),
    );

    const items = attention(p);
    const todo = items.length
      ? el(
          "ul",
          { class: "list" },
          items.map((a) =>
            el(
              "li",
              {},
              el("span", { class: `sev ${a.cls}` }, icon(SEVERITY_ICONS[a.cls])),
              el("span", { class: "grow" }, a.text),
              a.href ? goLink(a.href, "Open") : null,
            ),
          ),
        )
      : empty("All clear", "Nothing in the pipeline needs you right now.", { good: true });

    // App uploads have no connection: their counts read "-".
    const count = (s, n) => (isCloud(s) ? num(n || 0) : el("td", { class: "num dim" }, "-"));
    const sourceRows = (p.sources || []).map((s) => {
      const tr = el(
        "tr",
        { class: s.connections && s.connections.needs_reauth > 0 ? "row-bad" : null },
        el("td", {}, named(s.name || pname(s.slug))),
        el("td", {}, (s.kinds || []).map((k) => tag(KIND_LABELS[k] || k))),
        el("td", {}, s.enabled ? pill("ok", isCloud(s) ? "On" : "Always on") : pill("off", "Off")),
        count(s, s.connections && s.connections.active),
        count(s, s.broken),
        count(s, s.connections && s.connections.needs_reauth),
        el("td", {}, s.paused_until ? pill("warn", `Paused, resumes ${relTime(s.paused_until)}`) : el("span", { class: "dim" }, "-")),
      );
      return tenant.admin ? clickable(tr, `#/providers/${encodeURIComponent(s.slug)}`) : tr;
    });

    return el(
      "div",
      {},
      head("How data moves through this instance, from the providers to your systems."),
      flow,
      card({ title: "Needs attention", count: items.length || null }, todo),
      card(
        { title: "Sources", count: (p.sources || []).length, hint: "Providers that are on, and any provider that still holds connections." },
        table(
          ["Provider", "Kind", "Status", ["Connections", "num"], ["Unhealthy", "num"], ["Need reconnecting", "num"], "Rate limit"],
          sourceRows,
          empty("No source yet", "Turn a provider on, or upload from an app with the SDKs."),
        ),
      ),
    );
  }

  // -------------------------------------------------------------------------
  // Providers

  async function providersView() {
    if (!tenant.admin) {
      const b = await api("/v1/providers");
      return el(
        "div",
        {},
        pageHead({ eyebrow: "Pipeline", title: "Providers", sub: "The providers this instance has on." }),
        notice(
          "warn",
          "Only a key of the instance admin tenant can turn providers on and save their keys. ",
          el("code", {}, "ohs admin tenant list"),
          " shows which tenant that is.",
        ),
        card(
          { title: "On for this instance", count: (b.data || []).length },
          table(
            ["Provider", "Kind"],
            (b.data || []).map((p) => el("tr", {}, el("td", {}, named(p.name || pname(p.slug))), el("td", {}, (p.kinds || []).map((k) => tag(KIND_LABELS[k] || k))))),
            empty("No provider is on", "Ask the instance admin to turn one on."),
          ),
        ),
      );
    }
    const b = await api("/v1/admin/providers");
    const list = b.data || [];
    const cloud = list.filter((p) => p.status !== "built_in");
    const builtIn = list.filter((p) => p.status === "built_in");
    const on = cloud.filter((p) => p.status === "active").length;
    const errors = cloud.filter((p) => p.status === "error").length;

    const cloudRows = cloud.map((p) =>
      clickable(
        el(
          "tr",
          { class: p.status === "error" ? "row-bad" : p.status === "off" ? "row-off" : null },
          el("td", {}, el("a", { href: `#/providers/${encodeURIComponent(p.slug)}`, class: "named" }, mark(p.name), el("strong", {}, p.name))),
          el("td", {}, (p.kinds || []).map((k) => tag(KIND_LABELS[k] || k))),
          el("td", {}, pill(classifyProvider(p), providerStatusLabel(p.status))),
          el("td", { class: p.source ? null : "dim" }, p.source === "env" ? "Environment" : p.source === "console" ? "Console" : "-"),
          el("td", {}, when(p.updated_at)),
          el("td", { class: "row-actions dim" }, icon("chevron")),
        ),
        `#/providers/${encodeURIComponent(p.slug)}`,
      ),
    );
    const builtInRows = builtIn.map((p) =>
      el(
        "tr",
        {},
        el("td", {}, named(p.name)),
        el("td", {}, (p.kinds || []).map((k) => tag(KIND_LABELS[k] || k))),
        el("td", {}, pill("ok", "Always on")),
      ),
    );
    return el(
      "div",
      {},
      pageHead({ eyebrow: "Pipeline", title: "Providers", sub: "Turn on the providers your users connect to. Each one needs an app registered at the provider and its keys saved here." }),
      b.public_url
        ? null
        : notice(
            "warn",
            el("code", {}, "OHS_PUBLIC_URL"),
            " is not set: the redirect and notification addresses name localhost, which no provider can reach. Set it to this server's public address and restart.",
          ),
      stats([
        { label: "Cloud providers on", value: `${on} of ${cloud.length}` },
        { label: "Need keys", value: String(cloud.filter((p) => p.status === "not_configured").length) },
        { label: "With an error", value: String(errors), cls: errors ? "bad" : "" },
        { label: "App uploads", value: `${builtIn.length} always on` },
      ]),
      card({ title: "Cloud providers", count: cloud.length, hint: "Users connect their account; this server pulls their data or receives notifications." }, table(["Provider", "Kind", "Status", "Keys from", "Last change", ""], cloudRows, empty("No cloud provider in this build"))),
      card({ title: "App uploads", count: builtIn.length, hint: "Sent by the SDKs and apps to this server. Always on, no key needed." }, table(["Provider", "Kind", "Status"], builtInRows, empty("No app upload source in this build"))),
    );
  }

  async function providerView(slug) {
    if (!tenant.admin) return providersView();
    let p;
    try {
      p = await api(apiPath(["v1", "admin", "providers", slug]));
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) return el("div", {}, pageHead({ title: "Not found", crumbs: [["Providers", "#/providers"]] }), errorBox("No provider by this name in this build."));
      throw err;
    }
    const builtIn = p.status === "built_in";
    const fromEnv = p.source === "env";
    const path = apiPath(["v1", "admin", "providers", slug]);
    const saveChange = async (change, done) => {
      const next = await api(path, { method: "PATCH", body: change });
      flash(done(next));
      render();
    };

    // Show which key made the last change by its prefix, the name the
    // keys page uses. A revoked or unknown key falls back to its id.
    const [keys, pipe] = await Promise.all([p.updated_by ? settle("/v1/keys") : null, settle("/v1/pipeline")]);
    let changedBy = p.updated_by ?? "-";
    const k = keys && keys.ok && (keys.value.data || []).find((x) => x.id === p.updated_by);
    if (k) changedBy = `${k.prefix}...`;

    // The top of the page is a short frame: the provider, then how far its
    // setup is and how many users it serves.
    const keyed = p.status !== "not_configured";
    const registers = Boolean(p.urls && (p.urls.redirect || p.urls.inbound));
    const steps = builtIn
      ? []
      : [registers ? { name: "Register an app", ok: keyed } : null, { name: "Save its keys", ok: keyed }, { name: "Turn it on", ok: p.status === "active" }].filter(Boolean);
    const next = steps.find((x) => !x.ok);
    const src = pipe.ok ? (pipe.value.sources || []).find((x) => x.slug === slug) : null;
    const conns = src && src.connections;
    const tile = (label, value, footNode, cls = "") =>
      el("div", { class: "glass band-tile" }, el("div", { class: "label" }, label), el("div", {}, numeral(value, { cls }), el("div", { class: "tile-foot" }, footNode)));
    const tiles = builtIn
      ? [tile("Keys needed", "0", "Always on, nothing to set up")]
      : [
          tile(
            "Setup",
            `${steps.filter((x) => x.ok).length} of ${steps.length}`,
            next ? `Next: ${next.name.toLowerCase()}` : "Live for your users",
          ),
          tile(
            "Connections",
            conns ? formatCount(conns.active) : "-",
            conns && conns.needs_reauth > 0 ? pill("bad", `${formatCount(conns.needs_reauth)} to reconnect`) : conns ? "Users connected now" : "None yet",
          ),
        ];
    const head = [
      crumbsNav([["Providers", "#/providers"], [p.name, null]]),
      el(
        "section",
        { class: "frame band", "aria-label": p.name },
        scene(),
        el(
          "div",
          { class: `band-row tiles-${tiles.length}` },
          el(
            "div",
            { class: "glass band-main" },
            mark(p.name, "lg"),
            el(
              "div",
              { class: "band-text" },
              el("h1", {}, p.name),
              el(
                "p",
                { class: "sub" },
                builtIn
                  ? "Sent by the SDKs and apps. Always on."
                  : (p.kinds || []).includes("push")
                    ? "Users connect their account; the provider notifies this server of new data, which it then pulls."
                    : "Users connect their account; this server pulls their data on a schedule.",
              ),
              el("div", { class: "tags" }, pill(classifyProvider(p), providerStatusLabel(p.status)), (p.kinds || []).map((x) => tag(KIND_LABELS[x] || x))),
            ),
          ),
          tiles,
        ),
      ),
    ];
    const blocks = [];
    if (p.error) blocks.push(notice("bad", el("strong", {}, "The provider refused these keys. "), p.error));

    if (builtIn) {
      blocks.push(
        card(
          { title: "Nothing to set up" },
          body(
            el("p", {}, `${p.name} needs no key and no app registration: the SDK or app uploads to this server, which is always ready to receive it.`),
            facts([["Kind", kindsText(p.kinds)]]),
          ),
        ),
      );
      return el("div", {}, head, blocks);
    }

    // The setup, in the order an operator does it: register an app at the
    // provider, save its keys here, turn it on.
    let n = 0;
    const step = (ok) => {
      n++;
      return el("span", { class: `step-num${ok ? " done" : ""}`, "aria-label": ok ? `Step ${n}, done` : `Step ${n}` }, ok ? icon("check") : String(n).padStart(2, "0"));
    };
    if (registers) {
      const s = step(keyed);
      blocks.push(
        card(
          { title: `Register an app at ${p.name}`, lead: s, hint: `In ${p.name}'s developer portal, create an app and enter ${p.urls.redirect && p.urls.inbound ? "these addresses" : "this address"}.` },
          body(
            el(
              "dl",
              { class: "url-row" },
              p.urls.redirect ? [el("dt", {}, "Redirect URL"), el("dd", {}, copyField(p.urls.redirect))] : null,
              p.urls.inbound ? [el("dt", {}, "Notification URL"), el("dd", {}, copyField(p.urls.inbound))] : null,
            ),
          ),
        ),
      );
    }
    const s2 = step(keyed);
    blocks.push(keysCard(p, saveChange, s2, fromEnv));
    const s3 = step(p.status === "active");
    blocks.push(switchCard(p, saveChange, s3));

    if (p.sync) {
      blocks.push(
        card(
          { title: "Sync schedule", hint: "How this server pulls the provider for every connected user. Set by the connector, the same on every instance." },
          body(facts(syncSchedule(p.sync, (p.kinds || []).includes("push")))),
        ),
      );
    }
    blocks.push(
      card(
        { title: "Details" },
        body(
          facts([
            ["Keys from", fromEnv ? "Environment variables on the server" : p.source === "console" ? "This console, encrypted at rest" : "Not set"],
            ["Last change", p.updated_at ? [when(p.updated_at), " by key ", el("code", {}, changedBy)] : "-"],
          ]),
        ),
      ),
    );
    if (p.updated_at && !readOnly) {
      blocks.push(
        card(
          { title: "Forget saved settings", cls: "danger", hint: "Delete the keys and the switch saved in this console. Environment variables are not touched." },
          body(
            el(
              "div",
              { class: "actions" },
              button(
                "Forget saved settings",
                () =>
                  ask({
                    title: `Forget the settings of ${p.name}?`,
                    body: [
                      "The keys saved in this console are deleted. Without environment variables, the provider stops until its keys are saved again.",
                      "Connections and data are kept.",
                    ],
                    confirm: "Forget settings",
                    danger: true,
                    typed: p.slug,
                    run: async () => {
                      await api(path, { method: "DELETE" });
                      flash(`The saved settings of ${p.name} are gone.`);
                      render();
                    },
                  }),
                { kind: "danger" },
              ),
            ),
          ),
        ),
      );
    }
    return el("div", {}, head, blocks);
  }

  function keysCard(p, saveChange, lead, fromEnv) {
    const locked = readOnly || fromEnv;
    const fields = (p.keys || []).map((k) => {
      const input = el("input", {
        type: k.secret ? "password" : "text",
        name: k.name,
        id: `key-${k.name}`,
        autocomplete: "off",
        spellcheck: "false",
        disabled: locked,
        placeholder: k.secret ? (k.set ? "Saved. Type a new value to replace it." : "Not set") : k.set ? null : "Not set",
        "aria-describedby": `env-${k.name}`,
      });
      if (!k.secret && k.value) input.value = k.value;
      const clear = k.set && !locked ? el("input", { type: "checkbox", name: `clear-${k.name}` }) : null;
      const row = el(
        "div",
        { class: "setting" },
        el(
          "div",
          { class: "setting-label" },
          el("label", { class: "setting-name", for: `key-${k.name}` }, k.name),
          el(
            "div",
            { class: "setting-meta" },
            k.required ? tag("Required") : tag("Optional"),
            k.secret ? el("span", { class: "tag" }, "Secret") : null,
            k.set ? pill("ok", "Saved") : null,
          ),
          el("span", { class: "setting-env", id: `env-${k.name}` }, "or ", el("code", {}, k.env)),
        ),
        el("div", { class: "setting-control" }, input, clear ? el("label", { class: "check small muted" }, clear, "Remove this key") : null),
      );
      return { k, input, clear, row };
    });
    const formEl = el("form", { autocomplete: "off" });
    if (fromEnv) {
      formEl.append(
        body(notice("", "These keys come from environment variables on the server. Change them there, or remove them to manage the keys here. The switch below still applies.")),
      );
    }
    for (const f of fields) formEl.append(f.row);
    if (!locked) {
      const save = el("button", { type: "submit", class: "primary" }, "Save keys");
      formEl.append(foot(el("span", {}, icon("lock"), " Checked with the provider's own rules before anything is stored. Secrets are never shown again."), save));
      formEl.addEventListener("submit", async (ev) => {
        ev.preventDefault();
        const keys = keysPatch(
          fields.map((f) => ({
            name: f.k.name,
            secret: f.k.secret,
            set: f.k.set,
            value: f.k.value,
            input: f.input.value,
            clear: f.clear ? f.clear.checked : false,
          })),
        );
        if (!keys) {
          flash("Nothing changed.", "warn");
          return;
        }
        save.disabled = true;
        try {
          await saveChange({ keys }, (n) => `Keys saved. ${p.name} is ${providerStatusLabel(n.status).toLowerCase()}.`);
        } catch (err) {
          failure(err);
        } finally {
          save.disabled = false;
          for (const f of fields) if (f.k.secret) f.input.value = "";
        }
      });
    }
    return card({ title: "Save its keys", lead, hint: `The keys ${p.name} gave your app. Saved encrypted; each one can also come from its environment variable.` }, formEl);
  }

  // switchCard is the last step: whether the provider runs.
  function switchCard(p, saveChange, lead) {
    let text;
    let action = null;
    if (p.status === "active") {
      text = `${p.name} is on. Users can pick it on their connection link, and pulls run on schedule.`;
      action = button(
        "Turn off",
        () =>
          ask({
            title: `Turn ${p.name} off?`,
            body: [
              `New connections, pulls and notifications of ${p.name} stop on every process within 30 seconds.`,
              "Connections and the data already collected are kept: turning it on again resumes them.",
            ],
            confirm: "Turn off",
            danger: true,
            run: () => saveChange({ enabled: false }, () => `${p.name} is off.`),
          }),
        { kind: "danger" },
      );
    } else if (!p.enabled) {
      text = `${p.name} is off. Its connections are kept and resume when it is on.`;
      action = button("Turn on", () => saveChange({ enabled: true }, (n) => `${p.name} is ${providerStatusLabel(n.status).toLowerCase()}.`), {
        kind: "primary",
      });
    } else if (p.status === "error") {
      text = "Fix the error above, then save the keys again: it turns on as soon as they are accepted.";
    } else {
      text = `Save the required keys first: ${p.name} turns on as soon as they are accepted.`;
    }
    return card(
      { title: "Turn it on", lead },
      body(el("div", { class: "actions" }, el("span", { class: "spacer" }, text), action)),
    );
  }

  // -------------------------------------------------------------------------
  // Users

  async function usersView() {
    const stack = cursors.users;
    const page = Math.max(0, stack.length - 1);
    const b = await api(apiPath(["v1", "users"], { limit: 50, cursor: stack[page] }));
    const rows = (b.data || []).map((u) => {
      const href = `#/users/${encodeURIComponent(u.id)}`;
      return clickable(
        el(
          "tr",
          { class: u.status !== "active" ? "row-off" : null },
          el(
            "td",
            {},
            el(
              "span",
              { class: "named" },
              mark(u.external_ref || "?"),
              cell2(link(href, u.external_ref || "No reference"), el("code", {}, u.id)),
            ),
          ),
          el("td", {}, pill(classifyUser(u), humanize(u.status))),
          el("td", {}, when(u.created_at)),
          el("td", { class: "row-actions dim" }, icon("chevron")),
        ),
        href,
      );
    });
    const create = button(
      "New user",
      async () => {
        const ref = el("input", { type: "text", id: "new-ref", autocomplete: "off", spellcheck: "false", maxlength: "200", placeholder: "participant-042" });
        const u = await ask({
          title: "New user",
          body: ["A user is one person whose data this tenant collects. Next, send them a connection link from their page."],
          extra: el(
            "div",
            { class: "form-field" },
            el("label", { for: "new-ref" }, "Your reference (optional)"),
            ref,
            el("span", { class: "help" }, "An id from your own system, such as a study code. Never a name or an email."),
          ),
          confirm: "Create user",
          focus: ref,
          run: () => api("/v1/users", { method: "POST", body: ref.value.trim() ? { external_ref: ref.value.trim() } : {} }),
        });
        if (u && u.id) {
          flash("User created. Send them a connection link.");
          go(`#/users/${encodeURIComponent(u.id)}`);
        }
      },
      { kind: "primary", icon: "plus" },
    );
    return el(
      "div",
      {},
      pageHead({ eyebrow: "Pipeline", title: "Users", sub: "The people whose data this tenant collects. Each one connects their own accounts.", actions: [create] }),
      card(
        { title: "All users" },
        table(["User", "Status", "Created", ""], rows, empty("No user yet", readOnly ? null : "Create one, then send them a connection link.")),
        rows.length || page > 0 ? pager(stack, b.next_cursor, ["Previous", "Next"]) : null,
      ),
    );
  }

  async function userView(id) {
    let user;
    try {
      user = await api(apiPath(["v1", "users", id]));
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) return el("div", {}, pageHead({ title: "Not found", crumbs: [["Users", "#/users"]] }), errorBox("User not found."));
      throw err;
    }
    const base = ["v1", "users", id];
    const [conns, fresh, cov, runs] = await Promise.all([
      settle(apiPath([...base, "connections"])),
      settle(apiPath([...base, "freshness"])),
      settle(apiPath([...base, "coverage"])),
      settle(apiPath([...base, "sync-runs"], { limit: 10 })),
    ]);
    const label = user.external_ref || "Unnamed user";

    const actions = [
      button(
        "Connection link",
        async () => {
          const s = await api(apiPath([...base, "connect-sessions"]), { method: "POST", body: {} });
          await reveal({
            title: "Connection link",
            intro: "Send this link to the user. They pick their provider and consent with their own account.",
            value: s.url,
            note: `It expires ${relTime(s.expires_at)} (${absTime(s.expires_at)}). Anyone holding it can connect accounts to this user until then.`,
          });
        },
        { kind: "primary", icon: "link" },
      ),
      button(
        "Delete",
        () =>
          ask({
            title: "Delete this user and all their data?",
            body: ["Every record, raw file, connection and token of this user is erased. Providers are told to revoke access. This cannot be undone."],
            confirm: "Delete user",
            danger: true,
            typed: user.external_ref || user.id,
            run: async () => {
              await api(apiPath(base), { method: "DELETE" });
              flash("Deletion started. The user disappears once every record is erased.");
              go("#/users");
            },
          }),
        { kind: "danger" },
      ),
    ];

    const connList = conns.ok ? conns.value.data || [] : [];
    const connRows = connList.map((c) =>
      el(
        "tr",
        { class: c.status === "needs_reauth" ? "row-bad" : c.status === "disconnected" ? "row-off" : null },
        el("td", {}, named(pname(c.provider))),
        el("td", {}, pill(classifyConnection(c), humanize(c.status))),
        el("td", {}, healthCell(c.health)),
        el("td", {}, when(c.last_sync_at)),
        el("td", {}, when(c.created_at || c.connected_at)),
        readOnly
          ? null
          : el(
              "td",
              { class: "row-actions" },
              el(
                "div",
                { class: "actions" },
                c.status === "active"
                  ? button(
                      "Sync now",
                      async () => {
                        await api(apiPath([...base, "connections", c.provider, "sync"]), { method: "POST" });
                        flash(`A sync of ${pname(c.provider)} is queued.`);
                        render();
                      },
                      { kind: "small", icon: "refresh" },
                    )
                  : null,
                c.status !== "disconnected"
                  ? button(
                      "Disconnect",
                      () => {
                        const wipe = el("input", { type: "checkbox" });
                        return ask({
                          title: `Disconnect ${pname(c.provider)}?`,
                          body: ["Access is revoked at the provider and pulls stop. The user can connect again later."],
                          extra: el("label", { class: "check" }, wipe, "Also delete the data this provider sent"),
                          confirm: "Disconnect",
                          danger: true,
                          run: async () => {
                            await api(apiPath([...base, "connections", c.provider], { delete_data: wipe.checked ? "true" : null }), { method: "DELETE" });
                            flash(wipe.checked ? `${pname(c.provider)} disconnected; its data is being erased.` : `${pname(c.provider)} disconnected.`);
                            render();
                          },
                        });
                      },
                      { kind: "small danger" },
                    )
                  : null,
              ),
            ),
      ),
    );

    const runRows = runs.ok
      ? (runs.value.data || []).map((r) =>
          el(
            "tr",
            { class: r.status === "failed" ? "row-bad" : null },
            el("td", {}, named(pname(r.provider))),
            el("td", {}, tag(humanize(r.trigger))),
            el("td", {}, pill(classifyRun(r), humanize(r.status))),
            el("td", { class: "nowrap dim" }, formatWindow(r.window_start, r.window_end)),
            num(r.objects_stored),
            el("td", { class: r.error ? "err" : "dim" }, r.error ?? "-"),
            el("td", {}, when(r.created_at)),
          ),
        )
      : [];

    const freshRows = fresh.ok
      ? (fresh.value.data || []).map((s) => {
          const cls = classifyFreshness(s);
          return el(
            "tr",
            { class: cls === "bad" ? "row-bad" : null },
            el("td", {}, named(pname(s.provider))),
            el("td", {}, pill(cls, humanize(s.state))),
            el("td", {}, when(s.synced_through)),
            el("td", { class: "nowrap" }, s.lag_seconds === null || s.lag_seconds === undefined ? el("span", { class: "dim" }, "-") : formatAgo(s.lag_seconds)),
            el("td", {}, when(s.last_attempt_at)),
            num(s.consecutive_failures ?? 0),
            el("td", { class: s.last_error ? "err" : "dim" }, s.last_error ?? "-"),
          );
        })
      : [];

    const covSummary = cov.ok ? summarizeCoverage(cov.value) : null;
    const active = connList.filter((c) => c.status === "active").length;
    const reauth = connList.filter((c) => c.status === "needs_reauth").length;

    return el(
      "div",
      {},
      pageHead({
        title: label,
        lead: mark(label, "lg"),
        status: pill(classifyUser(user), humanize(user.status)),
        sub: [el("code", {}, user.id), " · created ", when(user.created_at)],
        crumbs: [["Users", "#/users"], [label, null]],
        actions,
      }),
      stats([
        { label: "Active connections", value: connList.length ? `${active} of ${connList.length}` : "0", cls: reauth ? "warn" : "" },
        { label: "Days with data", value: covSummary ? formatCount(covSummary.days) : "-" },
        { label: "Records", value: covSummary ? formatCount(covSummary.records) : "-" },
        { label: "Jobs waiting", value: cov.ok ? formatCount(cov.value.pending_jobs) : "-", cls: cov.ok && cov.value.pending_jobs > 0 ? "warn" : "" },
        { label: "Parked jobs", value: cov.ok ? formatCount(cov.value.parked_jobs) : "-", cls: cov.ok && cov.value.parked_jobs > 0 ? "bad" : "" },
      ]),
      card(
        { title: "Connections", count: connList.length || null },
        conns.ok
          ? table(
              ["Provider", "Status", "Health", "Last sync", "Connected", readOnly ? null : ""].filter((h) => h !== null),
              connRows,
              empty("No provider connected yet", readOnly ? null : "Create a connection link and send it to the user."),
            )
          : body(errorBox(conns.error)),
      ),
      card(
        { title: "Freshness", hint: "How current each source is, and why one is behind." },
        fresh.ok
          ? table(["Source", "State", "Synced through", "Lag", "Last attempt", ["Failures", "num"], "Last error"], freshRows, empty("No source yet"))
          : body(errorBox(fresh.error)),
      ),
      card(
        { title: "Recent sync runs" },
        runs.ok
          ? table(["Provider", "Trigger", "Status", "Window", ["Objects", "num"], "Error", "When"], runRows, empty("No sync run yet"))
          : body(errorBox(runs.error)),
      ),
      card({ title: "Coverage", hint: "Days with data, per provider." }, cov.ok ? coverageBlock(cov.value, covSummary) : body(errorBox(cov.error))),
      card(
        { title: "Health", hint: "Sleep, heart, activity, workouts and body measurements over the last 30 or 90 days. They load only when you ask, and each read is recorded in the audit log." },
        healthBlock(id),
      ),
      card({ title: "Data", hint: "Health values load only when you ask. Each read and export is recorded in the audit log." }, dataBlock(id)),
    );
  }

  function summarizeCoverage(c) {
    const byProvider = new Map();
    const dates = new Set();
    let records = 0;
    for (const d of c.days || []) {
      const p = byProvider.get(d.provider) || { days: 0, records: 0, first: d.date, last: d.date, synced: d.last_synced_at };
      p.days++;
      p.records += d.records || 0;
      records += d.records || 0;
      dates.add(d.date);
      if (d.date < p.first) p.first = d.date;
      if (d.date > p.last) p.last = d.date;
      if (d.last_synced_at > p.synced) p.synced = d.last_synced_at;
      byProvider.set(d.provider, p);
    }
    return { byProvider, days: dates.size, records };
  }

  function coverageBlock(c, sum) {
    const rows = [...sum.byProvider.entries()]
      .sort((a, b) => b[1].records - a[1].records)
      .map(([prov, p]) =>
        el(
          "tr",
          {},
          el("td", {}, named(pname(prov))),
          num(p.days),
          num(p.records),
          el("td", { class: "nowrap" }, p.first),
          el("td", { class: "nowrap" }, p.last),
          el("td", {}, when(p.synced)),
        ),
      );
    return [
      table(["Provider", ["Days", "num"], ["Records", "num"], "First day", "Last day", "Last synced"], rows, empty("No data has landed yet")),
      c.rejected_records > 0 ? foot(el("span", { class: "warn" }, `${formatCount(c.rejected_records)} records were rejected by processing.`)) : null,
      c.truncated ? foot("More than 10,000 days: the totals cover the earliest ones.") : null,
    ];
  }

  // dataBlock reads health values only when the operator asks: each read is
  // in the audit log as data.read or data.export.
  function dataBlock(id) {
    const base = ["v1", "users", id];
    const out = el("div", {});
    const act = (title, ic, fn) => {
      const b = el("button", { type: "button" }, icon(ic), title);
      b.addEventListener("click", async () => {
        b.disabled = true;
        try {
          await fn();
        } catch (err) {
          failure(err);
        } finally {
          b.disabled = false;
        }
      });
      return b;
    };
    const preview = (title, path) =>
      act(title, "eye", async () => {
        const b = await api(path);
        const rows = (b.data || b.entry || []).slice(0, 10);
        out.replaceChildren(
          el("div", { class: "subhead" }, `${title}: the first ${rows.length}`),
          rows.length ? el("pre", { class: "json" }, JSON.stringify(rows, null, 2)) : el("p", { class: "muted" }, "Nothing yet."),
        );
      });
    const file = (title, path, name) => act(title, "download", () => download(path, name));
    return body(
      el("div", { class: "subhead" }, "Preview"),
      el(
        "div",
        { class: "button-row" },
        preview("Daily summaries", apiPath([...base, "daily"], { limit: 10 })),
        preview("Sleep", apiPath([...base, "sleep"], { limit: 10 })),
        preview("Workouts", apiPath([...base, "workouts"], { limit: 10 })),
      ),
      el("div", { class: "subhead" }, "Standard formats"),
      el(
        "div",
        { class: "button-row" },
        file("FHIR R4 Bundle", apiPath([...base, "fhir", "Patient", id, "$everything"], { limit: 1000 }), `fhir-${id}.json`),
        file("Open mHealth", apiPath([...base, "omh", "data-points"], { limit: 1000 }), `omh-${id}.json`),
        file("Full archive (zip)", apiPath([...base, "archive"]), `archive-${id}.zip`),
      ),
      el("div", { class: "subhead" }, "CSV"),
      el(
        "div",
        { class: "button-row" },
        EXPORTS.map(([d, title]) => file(title, apiPath([...base, "exports", d], { format: "csv" }), `${d}-${id}.csv`)),
      ),
      el("p", { class: "muted small note" }, "FHIR and Open mHealth hold the first 1,000 records; the archive holds everything."),
      out,
    );
  }

  // healthBlock draws the user's health as charts. Like dataBlock it reads
  // health values only when the operator asks, through the API, so each
  // read is in the audit log as data.read. Picking the other range once the
  // charts show reads it at once; until then it only picks.
  function healthBlock(id) {
    let days = HEALTH_RANGES[0];
    let shown = false;
    let seq = 0;
    const out = el("div", { class: "charts-wrap" });
    const load = el("button", { type: "button", class: "primary" }, icon("eye"), "Show charts");
    const picks = HEALTH_RANGES.map((n) => {
      const b = el("button", { type: "button", "aria-pressed": String(n === days) }, `${n} days`);
      b.addEventListener("click", () => {
        if (n === days) return;
        days = n;
        for (const [i, p] of picks.entries()) p.setAttribute("aria-pressed", String(HEALTH_RANGES[i] === n));
        if (shown) read();
      });
      return b;
    });
    async function read() {
      const mine = ++seq;
      load.disabled = true;
      out.setAttribute("aria-busy", "true");
      try {
        const charts = await readHealth(id, days);
        if (mine !== seq) return; // a later range won
        out.replaceChildren(charts);
        shown = true;
        load.replaceChildren(icon("refresh"), "Reload");
        load.className = "";
      } catch (err) {
        failure(err);
      } finally {
        if (mine === seq) {
          load.disabled = false;
          out.removeAttribute("aria-busy");
        }
      }
    }
    load.addEventListener("click", read);
    return body(el("div", { class: "chart-bar" }, el("div", { class: "segmented", role: "group", "aria-label": "Range" }, picks), load), out);
  }

  // readHealth reads the last n days of a user from the documented routes:
  // daily summaries (steps, active energy), sleep sessions, workouts, and
  // the daily statistics of resting heart rate, heart rate variability,
  // weight and body fat in the zone the records were made in. A failing
  // read shows in its own chart.
  async function readHealth(id, n) {
    const base = ["v1", "users", id];
    const range = healthRange(n);
    const query = { start: range.start, end: range.end, limit: 1000 };
    const [daily, sleep, workouts] = await Promise.all([
      settle(apiPath([...base, "daily"], query)),
      settle(apiPath([...base, "sleep"], { ...query, start: range.before })),
      settle(apiPath([...base, "workouts"], query)),
    ]);
    const data = (r) => (r.ok ? r.value.data || [] : []);
    const zone = recordZone(data(daily), data(sleep), data(workouts));
    const stats = (type) => settle(apiPath([...base, "samples", "stats"], { type, interval: "day", tz: zone, start: range.start, end: range.end }));
    const [rhr, rmssd, weight, fat] = await Promise.all([stats("resting_heart_rate"), stats("hrv_rmssd"), stats("body_mass"), stats("body_fat_percentage")]);
    // RMSSD and SDNN never share a chart: SDNN only when there is no RMSSD.
    let hrv = rmssd;
    let hrvKind = "RMSSD";
    if (!rmssd.ok || !(rmssd.value.buckets || []).length) {
      const sdnn = await stats("hrv_sdnn");
      if (sdnn.ok && (sdnn.value.buckets || []).length) [hrv, hrvKind] = [sdnn, "SDNN"];
      else if (rmssd.ok) hrvKind = null;
    }
    return healthCharts({ n, days: range.days, zone, daily, sleep, workouts, rhr, hrv, hrvKind, weight, fat });
  }

  // -------------------------------------------------------------------------
  // Jobs

  async function jobsView() {
    const [runs, jobs] = await Promise.all([
      settle(apiPath(["v1", "pipeline", "sync-runs"], { status: "failed", limit: 50 })),
      settle(apiPath(["v1", "pipeline", "etl-jobs"], { status: "parked", limit: 100 })),
    ]);
    const parked = jobs.ok ? jobs.value.data || [] : [];
    const failed = runs.ok ? runs.value.data || [] : [];
    const retryAll = parked.length
      ? button(
          "Retry all",
          () =>
            ask({
              title: "Retry every parked job?",
              body: ["Each job is queued again with fresh attempts. A job that fails for the same reason parks again after 5 attempts."],
              confirm: "Retry all",
              run: async () => {
                const r = await api("/v1/pipeline/etl-jobs/retry", { method: "POST", body: {} });
                flash(`${formatCount(r.retried)} ${r.retried === 1 ? "job" : "jobs"} queued again.`);
                render();
              },
            }),
          { kind: "primary", icon: "refresh" },
        )
      : null;
    const userCell = (uid) => el("td", {}, link(`#/users/${encodeURIComponent(uid)}`, el("code", {}, uid.slice(0, 8))));
    const jobRows = parked.map((j) =>
      el(
        "tr",
        {},
        el("td", {}, el("code", {}, `#${j.id}`)),
        userCell(j.user_id),
        el("td", {}, named(pname(j.provider))),
        num(j.attempts),
        el("td", { class: "err" }, j.error ?? "-"),
        el("td", {}, when(j.created_at)),
        readOnly
          ? null
          : el(
              "td",
              { class: "row-actions" },
              button(
                "Retry",
                async () => {
                  await api(apiPath(["v1", "pipeline", "etl-jobs", j.id, "retry"]), { method: "POST" });
                  flash(`Job #${j.id} queued again.`);
                  render();
                },
                { kind: "small" },
              ),
            ),
      ),
    );
    const runRows = failed.map((r) =>
      el(
        "tr",
        {},
        userCell(r.user_id),
        el("td", {}, named(pname(r.provider))),
        el("td", {}, tag(humanize(r.trigger))),
        num(r.attempts),
        el("td", { class: "err" }, r.error ?? "-"),
        el("td", {}, when(r.finished_at || r.created_at)),
      ),
    );
    return el(
      "div",
      {},
      pageHead({ eyebrow: "Pipeline", title: "Jobs", sub: "Work that stopped and waits for you. Everything else retries on its own." }),
      card(
        {
          title: "Parked processing jobs",
          count: jobs.ok ? parked.length : null,
          hint: "A raw file that failed to process 5 times. Fix the cause (often a provider change or a full disk), then retry. The raw file is kept.",
          actions: retryAll,
        },
        jobs.ok
          ? table(["Job", "User", "Provider", ["Attempts", "num"], "Error", "Created", readOnly ? null : ""].filter((h) => h !== null), jobRows, empty("No parked job", "Processing is keeping up.", { good: true }))
          : body(errorBox(jobs.error)),
      ),
      card(
        { title: "Failed sync runs", count: runs.ok ? failed.length : null, hint: "Pulls that gave up. The next scheduled sync tries again; Sync now on the user's page tries at once." },
        runs.ok ? table(["User", "Provider", "Trigger", ["Attempts", "num"], "Error", "Ended"], runRows, empty("No failed sync run", "Every pull is going through.", { good: true })) : body(errorBox(runs.error)),
      ),
    );
  }

  // -------------------------------------------------------------------------
  // Webhooks

  function eventTypePicker(selected) {
    const boxes = EVENT_TYPES.map((t) => {
      const box = el("input", { type: "checkbox", value: t, checked: selected.includes(t) });
      return { t, box, label: el("label", { class: "check small" }, box, el("code", {}, t)) };
    });
    return {
      node: el("fieldset", { class: "types" }, el("legend", {}, "Event types ", el("span", { class: "muted" }, "(none checked: every type)")), el("div", { class: "type-grid" }, boxes.map((b) => b.label))),
      value: () => boxes.filter((b) => b.box.checked).map((b) => b.t),
    };
  }

  function endpointFields(hook) {
    const url = el("input", { type: "url", id: "wh-url", class: "mono", required: true, placeholder: "https://example.com/hooks/ohs", autocomplete: "off" });
    const desc = el("input", { type: "text", id: "wh-desc", placeholder: "Research warehouse", autocomplete: "off" });
    if (hook) {
      url.value = hook.url;
      desc.value = hook.description || "";
    }
    const types = eventTypePicker(hook ? hook.event_types || [] : []);
    return {
      url,
      node: el(
        "div",
        { class: "form" },
        el("div", { class: "form-field" }, el("label", { for: "wh-url" }, "Endpoint URL"), url, el("span", { class: "help" }, "HTTPS. Each delivery is signed; verify it with the signing secret.")),
        el("div", { class: "form-field" }, el("label", { for: "wh-desc" }, "Description (optional)"), desc),
        types.node,
      ),
      value: () => ({ url: url.value.trim(), description: desc.value.trim(), event_types: types.value() }),
    };
  }

  async function webhooksView() {
    const b = await api("/v1/webhooks");
    const rows = (b.data || []).map((w) => {
      const href = `#/webhooks/${encodeURIComponent(w.id)}`;
      const cls = classifyWebhook(w);
      return clickable(
        el(
          "tr",
          { class: cls === "bad" ? "row-bad" : null },
          el("td", {}, cell2(link(href, w.description || "Endpoint"), el("code", {}, w.url))),
          el("td", { class: "dim" }, (w.event_types || []).length ? `${w.event_types.length} types` : "Every type"),
          el("td", {}, pill(cls, webhookLabel(w))),
          el("td", {}, when(w.last_success_at)),
          el("td", { class: w.failing_since ? "err" : "dim" }, w.failing_since ? when(w.failing_since) : "-"),
          el("td", { class: "row-actions dim" }, icon("chevron")),
        ),
        href,
      );
    });
    const create = button(
      "New endpoint",
      async () => {
        const f = endpointFields(null);
        const w = await ask({
          title: "New webhook endpoint",
          body: ["This server sends a signed event to this URL when data is written or deleted, or a connection breaks."],
          extra: f.node,
          confirm: "Create endpoint",
          wide: true,
          focus: f.url,
          run: () => api("/v1/webhooks", { method: "POST", body: f.value() }),
        });
        if (!w || !w.id) return;
        await reveal({
          title: "Signing secret",
          intro: "Verify every delivery with this secret (see docs/webhooks.md).",
          value: w.secret,
          note: "It is shown this once. Store it with your receiver now.",
        });
        go(`#/webhooks/${encodeURIComponent(w.id)}`);
      },
      { kind: "primary", icon: "plus" },
    );
    return el(
      "div",
      {},
      pageHead({ eyebrow: "Pipeline", title: "Webhooks", sub: "Endpoints this server notifies when data is written or deleted, or a connection breaks.", actions: [create] }),
      card(
        { title: "Endpoints", count: (b.data || []).length || null },
        table(["Endpoint", "Events", "Status", "Last success", "Failing since", ""], rows, empty("No webhook endpoint", "Add one to push new data to your systems as it lands.")),
      ),
    );
  }

  async function webhookView(id) {
    const [hook, deliveries] = await Promise.all([
      api(apiPath(["v1", "webhooks", id])).catch((err) => {
        if (err instanceof ApiError && err.status === 404) return null;
        throw err;
      }),
      settle(apiPath(["v1", "webhooks", id, "deliveries"], { limit: 50 })),
    ]);
    if (!hook) return el("div", {}, pageHead({ title: "Not found", crumbs: [["Webhooks", "#/webhooks"]] }), errorBox("Webhook endpoint not found."));
    const path = ["v1", "webhooks", id];
    const title = hook.description || "Endpoint";
    const actions = [
      button(
        "Send test",
        async () => {
          await api(apiPath([...path, "test"]), { method: "POST", body: {} });
          flash("A test event is queued for delivery.");
          render();
        },
        { icon: "play" },
      ),
      button("Rotate secret", () =>
        ask({
          title: "Rotate the signing secret?",
          body: ["Deliveries are signed with the new secret at once. Update your receiver right after."],
          confirm: "Rotate",
          run: () => api(apiPath([...path, "rotate-secret"]), { method: "POST" }),
        }).then((r) => (r && r.secret ? reveal({ title: "New signing secret", intro: "Update your receiver with it now.", value: r.secret, note: "It is shown this once." }) : null)),
      ),
      hook.status === "active"
        ? button("Turn off", async () => {
            await api(apiPath(path), { method: "PATCH", body: { enabled: false } });
            flash("Endpoint turned off.");
            render();
          })
        : button(
            "Turn on",
            async () => {
              await api(apiPath(path), { method: "PATCH", body: { enabled: true } });
              flash("Endpoint turned on.");
              render();
            },
            { kind: "primary" },
          ),
      button(
        "Delete",
        () =>
          ask({
            title: "Delete this endpoint?",
            body: ["It stops receiving events and its delivery history is deleted."],
            confirm: "Delete endpoint",
            danger: true,
            typed: hook.url,
            run: async () => {
              await api(apiPath(path), { method: "DELETE" });
              flash("Endpoint deleted.");
              go("#/webhooks");
            },
          }),
        { kind: "danger" },
      ),
    ];

    let edit = null;
    if (!readOnly) {
      const f = endpointFields(hook);
      const submit = el("button", { type: "submit", class: "primary" }, "Save changes");
      edit = el("form", { autocomplete: "off" }, body(f.node), foot(el("span", {}, "Changes apply to the next delivery."), submit));
      edit.addEventListener("submit", async (ev) => {
        ev.preventDefault();
        submit.disabled = true;
        try {
          await api(apiPath(path), { method: "PATCH", body: f.value() });
          flash("Endpoint saved.");
          render();
        } catch (err) {
          failure(err);
        } finally {
          submit.disabled = false;
        }
      });
    }

    const rows = deliveries.ok
      ? (deliveries.value.data || []).map((d) => {
          const cls = classifyDelivery(d);
          const finished = d.status === "done" || d.status === "parked";
          return el(
            "tr",
            { class: cls === "bad" ? "row-bad" : null },
            el("td", {}, el("code", {}, `#${d.id}`)),
            el("td", {}, tag(d.event_type, "mono")),
            el("td", {}, pill(cls, humanize(d.status))),
            num(d.attempts ?? 0),
            el("td", { class: "num" }, d.last_status_code === null || d.last_status_code === undefined ? "-" : String(d.last_status_code)),
            el("td", { class: d.last_error ? "err" : "dim" }, d.last_error ?? "-"),
            el("td", {}, when(d.created_at)),
            el("td", {}, when(d.delivered_at || d.next_attempt_at)),
            readOnly
              ? null
              : el(
                  "td",
                  { class: "row-actions" },
                  finished
                    ? el(
                        "div",
                        { class: "actions" },
                        button(
                          "Retry",
                          async () => {
                            await api(apiPath([...path, "deliveries", d.id, "retry"]), { method: "POST" });
                            flash(`Delivery #${d.id} queued again.`);
                            render();
                          },
                          { kind: "small" },
                        ),
                        button(
                          "Replay",
                          async () => {
                            await api(apiPath([...path, "deliveries", d.id, "replay"]), { method: "POST" });
                            flash(`The event of delivery #${d.id} is sent again as a new delivery.`);
                            render();
                          },
                          { kind: "small" },
                        ),
                      )
                    : null,
                ),
          );
        })
      : [];
    return el(
      "div",
      {},
      pageHead({
        title,
        status: pill(classifyWebhook(hook), webhookLabel(hook)),
        sub: el("code", {}, hook.url),
        crumbs: [["Webhooks", "#/webhooks"], [title, null]],
        actions,
      }),
      hook.disabled_reason ? notice("bad", el("strong", {}, "Turned off: "), hook.disabled_reason) : null,
      stats([
        { label: "Last success", value: hook.last_success_at ? relTime(hook.last_success_at) : "Never" },
        { label: "Failing since", value: hook.failing_since ? relTime(hook.failing_since) : "Not failing", cls: hook.failing_since ? "bad" : "" },
        { label: "Event types", value: (hook.event_types || []).length ? String(hook.event_types.length) : "Every type" },
      ]),
      card(
        { title: "Recent deliveries", count: deliveries.ok ? (deliveries.value.data || []).length || null : null, hint: "The last 30 days. Retry sends a delivery again; Replay sends its event as a new delivery." },
        deliveries.ok
          ? table(
              ["Delivery", "Event", "Status", ["Attempts", "num"], ["Code", "num"], "Last error", "Created", "Delivered or next", readOnly ? null : ""].filter((h) => h !== null),
              rows,
              empty("No delivery in the last 30 days", readOnly ? null : "Send a test event to check the endpoint."),
            )
          : body(errorBox(deliveries.error)),
      ),
      edit ? card({ title: "Settings" }, edit) : null,
    );
  }

  // -------------------------------------------------------------------------
  // API keys

  async function keysView() {
    const b = await api("/v1/keys");
    const keys = b.data || [];
    const rows = keys.map((k) =>
      el(
        "tr",
        { class: k.revoked_at ? "row-off" : null },
        el("td", {}, el("span", { class: "named" }, icon("key"), el("code", {}, `${k.prefix}...`), k.current ? pill("accent", "This session") : null)),
        el("td", {}, when(k.created_at)),
        el("td", {}, k.last_used_at ? when(k.last_used_at) : el("span", { class: "dim" }, "Never")),
        el("td", {}, k.revoked_at ? pill("off", "Revoked") : pill("ok", "Active")),
        readOnly
          ? null
          : el(
              "td",
              { class: "row-actions" },
              !k.revoked_at && !k.current
                ? button(
                    "Revoke",
                    () =>
                      ask({
                        title: "Revoke this key?",
                        body: ["Every service using it is refused at once. This cannot be undone."],
                        confirm: "Revoke key",
                        danger: true,
                        typed: k.prefix,
                        run: async () => {
                          await api(apiPath(["v1", "keys", k.id]), { method: "DELETE" });
                          flash("Key revoked.");
                          render();
                        },
                      }),
                    { kind: "small danger" },
                  )
                : null,
            ),
      ),
    );
    const create = button(
      "Create key",
      async () => {
        const k = await api("/v1/keys", { method: "POST" });
        await reveal({
          title: "New API key",
          intro: "Store it in your secret manager now. Only a hash is kept on the server.",
          value: k.key,
          note: "It is shown this once. To rotate: deploy the new key, then revoke the old one here.",
        });
        render();
      },
      { kind: "primary", icon: "plus" },
    );
    return el(
      "div",
      {},
      pageHead({ eyebrow: "Access", title: "API keys", sub: `Keys of ${tenant.name}. A key can do everything this console does: treat each one as a password.`, actions: [create] }),
      card(
        { title: "Keys", count: keys.filter((k) => !k.revoked_at).length },
        table(["Key", "Created", "Last used", "Status", readOnly ? null : ""].filter((h) => h !== null), rows, empty("No key")),
        readOnly ? null : foot("The key of this session cannot revoke itself: sign in with another key first."),
      ),
    );
  }

  // -------------------------------------------------------------------------
  // Audit log

  async function auditView() {
    const stack = cursors.audit;
    const page = Math.max(0, stack.length - 1);
    const [b, keys] = await Promise.all([api(apiPath(["v1", "audit"], { limit: 50, cursor: stack[page], action: auditAction })), settle("/v1/keys")]);
    const prefixes = new Map(keys.ok ? (keys.value.data || []).map((k) => [k.id, k.prefix]) : []);
    const filter = el(
      "select",
      { "aria-label": "Action" },
      [
        ["", "Every action"],
        ["data.", "Data reads and exports"],
        ["user.", "Users"],
        ["connection.", "Connections"],
        ["consent.", "Consent"],
        ["webhook.", "Webhooks"],
        ["key.", "API keys"],
        ["config.", "Settings"],
        ["share.", "Share links"],
        ["tenant.", "Tenants"],
      ].map(([v, t]) => el("option", { value: v, selected: v === auditAction }, t)),
    );
    filter.addEventListener("change", () => {
      auditAction = filter.value;
      cursors.audit = [null];
      render();
    });
    const actor = (a) => {
      if (!a) return "-";
      if (a.type === "api_key" && prefixes.has(a.id)) return el("code", {}, `${prefixes.get(a.id)}...`);
      return [humanize(a.type), a.id ? [" ", el("code", { class: "dim" }, String(a.id).slice(0, 8))] : null];
    };
    const route = (r) => {
      if (!r) return el("span", { class: "dim" }, "-");
      const [method, ...rest] = String(r).split(" ");
      return rest.length ? el("span", { class: "named" }, tag(method, "mono"), el("code", { class: "dim" }, rest.join(" "))) : el("code", {}, r);
    };
    const rows = (b.data || []).map((ev) =>
      el(
        "tr",
        { class: ev.status >= 500 ? "row-bad" : ev.status >= 400 ? "row-warn" : null },
        el("td", {}, when(ev.at)),
        el("td", {}, tag(ev.action, "mono")),
        el("td", { class: "nowrap" }, actor(ev.actor)),
        el("td", {}, ev.user_id ? link(`#/users/${encodeURIComponent(ev.user_id)}`, el("code", {}, ev.user_id.slice(0, 8))) : el("span", { class: "dim" }, "-")),
        el("td", {}, route(ev.route)),
        el("td", { class: `num ${ev.status >= 400 ? "warn" : "dim"}` }, ev.status === null || ev.status === undefined ? "-" : String(ev.status)),
        el("td", {}, Object.entries(ev.detail || {}).map(([k, v]) => tag(`${k}: ${typeof v === "string" ? v : JSON.stringify(v)}`, "mono"))),
      ),
    );
    return el(
      "div",
      {},
      pageHead({ eyebrow: "Access", title: "Audit log", sub: "Who read, exported, changed or deleted what, with which key. Kept for OHS_AUDIT_RETENTION_DAYS (a year by default).", actions: [filter] }),
      card(
        { title: "Events" },
        table(["When", "Action", "Key", "User", "Request", ["Status", "num"], "Detail"], rows, empty("No event", auditAction ? "Nothing matches this filter." : null)),
        rows.length || page > 0 ? pager(stack, b.next_cursor, ["Newer", "Older"]) : null,
      ),
    );
  }

  render();
}

if (typeof document !== "undefined") {
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", main);
  else main();
}
