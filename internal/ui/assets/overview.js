// Every host at once: each connection with its CPU and memory in use and
// its machines, and one list of every machine on every connected host
// (search, filter by state and host, sort), with a host list's actions,
// and start, shut down and force off for several machines at once.
import { call } from "./rpc.js";
import { t } from "./i18n.js";
import { $, esc, toast, fail, kib, confirmDanger, icon, errorCard, emptyState, skeleton } from "./util.js";
import { watch, lastStats, memUsed } from "./monitor.js";
import { sideButton } from "./layout.js";
import { rowActions, menuOpen } from "./menu.js";

// app: what a host's list does with a machine (power, openConsole,
// openXML, deleteMachine), open(conn, d) its details page, host(conn) a
// host's own view, shown() whether the overview is on screen.
let app = null;
export function initOverview(hooks) { app = hooks; }

// data: the last Overview; view: the list's search, filters, sort and the
// machines picked (by "conn/uuid").
let data = null, loadErr = "";
const view = { q: "", state: "", host: "", sort: "name", desc: false, picked: new Set() };
const key = (m) => m.connection + "/" + m.uuid;

let timer = null;
export function reloadOverview() {
  clearTimeout(timer);
  timer = setTimeout(load, 150);
}

async function load() {
  try {
    data = await call("Overview");
    loadErr = "";
  } catch (e) {
    loadErr = String(e.message || e);
  }
  const keys = new Set((data?.machines || []).map(key));
  for (const k of view.picked) if (!keys.has(k)) view.picked.delete(k);
  if (app.shown()) draw();
}

export function renderOverview(main) {
  if (main.dataset.view !== "overview") {
    main.dataset.view = "overview";
    main.innerHTML = `<div class="head"><div class="lead">${sideButton()}<div><h1>${t("所有主机")}</h1>
        <div class="meta">${t("每个连接的主机, 和它们上面的所有虚拟机")}</div></div></div>
        <div class="actions"><button id="ov-reload">${icon("refresh-cw")}${t("刷新")}</button></div></div>
      <div id="ov-err"></div>
      <div class="hosts" id="ov-hosts"></div>
      <div class="toolbar ov-filter">
        <input type="search" id="ov-q" placeholder="${esc(t("搜索名称"))}" value="${esc(view.q)}" spellcheck="false">
        <select id="ov-state"></select><select id="ov-host"></select>
        <span class="sep"></span>
        <span id="ov-picked" class="mute"></span>
        <button data-bulk="start">${icon("play")}${t("start")}</button><button data-bulk="shutdown">${icon("power")}${t("shutdown")}</button><button data-bulk="force-off" class="del">${icon("power-off")}${t("force-off")}</button>
      </div>
      <div id="ov-list"></div>`;
    $("#ov-reload").onclick = load;
    $("#ov-q").oninput = (e) => { view.q = e.target.value; drawList(); };
    $("#ov-state").onchange = (e) => { view.state = e.target.value; drawList(); };
    $("#ov-host").onchange = (e) => { view.host = e.target.value; drawList(); };
    main.querySelectorAll("[data-bulk]").forEach((b) => { b.onclick = () => bulk(b.dataset.bulk); });
  }
  if (!data) load();
  draw();
}

function draw() {
  if (!$("#ov-hosts")) return;
  $("#ov-err").innerHTML = loadErr ? errorCard(t("读不到所有主机"), loadErr) : "";
  drawHosts();
  drawList();
}

function drawHosts() {
  if (!data) { $("#ov-hosts").innerHTML = `<div class="card">${skeleton()}</div>`; return; }
  $("#ov-hosts").innerHTML = data.hosts.length ? data.hosts.map((h) => {
    const u = h.usage;
    const cpu = u && u.cpu_percent != null ? Math.round(u.cpu_percent) : null;
    const mem = u && u.used_kib && u.memory_kib ? Math.round((u.used_kib / u.memory_kib) * 100) : null;
    const bar = (pct) => `<div class="meter"><div style="width:${Math.min(100, pct ?? 0)}%"></div></div>`;
    return `<div class="card hostcard${view.host === h.id ? " on" : ""}" data-host="${esc(h.id)}" role="button" tabindex="0" aria-pressed="${view.host === h.id}" aria-label="${esc(t("只看 {name} 上的虚拟机", { name: h.name }))}">
      <h3><span class="dot ${esc(h.host_key ? "pending" : h.status)}"></span> ${esc(h.name)}</h3>
      ${h.status !== "connected" ? `<p class="mute">${esc(h.status === "idle" || h.status === "closed" ? t("未连接") : h.status)}${h.error ? ": " + esc(h.error) : ""}</p>
        <div class="buttons"><button data-connect="${esc(h.id)}">${icon("refresh-cw")}${t("连上")}</button></div>`
      : `<dl class="kv">
        <dt>CPU</dt><dd>${cpu == null ? "–" : cpu + "%"}${bar(cpu)}</dd>
        <dt>${t("内存")}</dt><dd>${u && u.used_kib ? esc(`${kib(u.used_kib)} / ${kib(u.memory_kib)}`) : "–"}${bar(mem)}</dd>
        <dt>${t("虚拟机")}</dt><dd>${u ? esc(t("{run} 运行 / 共 {all}", { run: u.running, all: u.machines })) : "–"}</dd>
      </dl>${h.list_error ? `<p class="hint err">${esc(h.list_error)}</p>` : ""}
      <div class="buttons"><button data-open="${esc(h.id)}">${icon("chevron-right")}${t("打开")}</button></div>`}
    </div>`;
  }).join("") : emptyState({ small: true, icon: "server", title: t("还没有连接"), text: t("在侧栏里用 + 添加一台主机。") });
  $("#ov-hosts").querySelectorAll("[data-host]").forEach((el) => {
    el.onclick = (e) => {
      if (e.target.closest("button")) return;
      view.host = view.host === el.dataset.host ? "" : el.dataset.host; // a host's card filters the list by it
      draw();
    };
  });
  $("#ov-hosts").querySelectorAll("[data-open]").forEach((b) => { b.onclick = () => app.host(b.dataset.open); });
  $("#ov-hosts").querySelectorAll("[data-connect]").forEach((b) => {
    b.onclick = async () => { try { await call("Connect", b.dataset.connect); } catch (e) { fail(e); } };
  });
}

const states = ["running", "shutoff", "paused", "crashed"];
const hostName = (id) => data?.hosts.find((h) => h.id === id)?.name || id;

// shownMachines are the machines the search and filters leave, sorted.
function shownMachines() {
  const q = view.q.trim().toLowerCase();
  const list = (data?.machines || []).filter((m) =>
    (!q || m.name.toLowerCase().includes(q) || (m.title || "").toLowerCase().includes(q)) &&
    (!view.state || (view.state === "other" ? !states.includes(m.state) : m.state === view.state)) &&
    (!view.host || m.connection === view.host));
  const val = {
    name: (m) => m.name.toLowerCase(),
    host: (m) => hostName(m.connection).toLowerCase() + "\0" + m.name.toLowerCase(),
    state: (m) => m.state,
    vcpus: (m) => m.vcpus,
    memory: (m) => m.max_memory_kib,
    cpu: (m) => lastStats(m.connection, m.uuid)?.cpu_percent ?? -1,
  }[view.sort];
  list.sort((a, b) => {
    const x = val(a), y = val(b);
    const c = x < y ? -1 : x > y ? 1 : 0;
    return view.desc ? -c : c;
  });
  return list;
}

function drawList() {
  if (!data || !$("#ov-list")) return;
  const opt = (v, label, sel) => `<option value="${esc(v)}"${v === sel ? " selected" : ""}>${esc(label)}</option>`;
  $("#ov-state").innerHTML = opt("", t("全部状态"), view.state) + states.map((s) => opt(s, t(s), view.state)).join("") + opt("other", t("其他"), view.state);
  $("#ov-host").innerHTML = opt("", t("全部主机"), view.host) + data.hosts.map((h) => opt(h.id, h.name, view.host)).join("");
  const list = shownMachines();
  const picked = list.filter((m) => view.picked.has(key(m)));
  $("#ov-picked").textContent = picked.length ? t("选中 {n} 台", { n: picked.length }) : t("勾选虚拟机后可一起操作");
  document.querySelectorAll("[data-bulk]").forEach((b) => { b.disabled = picked.length === 0; });
  if (!data.machines.length) {
    $("#ov-list").innerHTML = emptyState({ small: true, icon: "monitor", title: t("已连接的主机上没有虚拟机"), text: t("打开一台主机, 在那里新建虚拟机。") });
    return;
  }
  const th = (k, label, cls = "") => `<th class="sort${cls}" data-sort="${k}">${esc(label)}${view.sort === k ? (view.desc ? " ↓" : " ↑") : ""}</th>`;
  $("#ov-list").innerHTML = `<div class="tablewrap"><table class="machines all"><thead><tr>
      <th><input type="checkbox" id="ov-all"${picked.length && picked.length === list.length ? " checked" : ""} aria-label="${esc(t("全选"))}"></th>
      ${th("name", t("名称"))}${th("host", t("主机"), " opt2")}${th("state", t("状态"))}${th("vcpus", "vCPU", " opt")}${th("memory", t("内存"), " opt")}${th("cpu", "CPU")}<th class="opt">${t("内存使用")}</th><th></th>
    </tr></thead><tbody>${list.map((m) => {
      const st = m.state === "running" ? lastStats(m.connection, m.uuid) : null;
      const k = key(m);
      return `<tr class="open" data-k="${esc(k)}">
        <td><input type="checkbox" data-pick="${esc(k)}"${view.picked.has(k) ? " checked" : ""}></td>
        <td><a class="name" href="#" data-k="${esc(k)}">${esc(m.name)}</a>${m.persistent ? "" : `<small>${t("临时")}</small>`}</td>
        <td class="opt2">${esc(hostName(m.connection))}</td>
        <td><span class="badge ${esc(m.state)}">${esc(t(m.state))}</span>${m.saved ? `<small>${t("已保存")}</small>` : ""}</td>
        <td class="num opt">${m.vcpus || "–"}</td>
        <td class="num opt">${kib(m.max_memory_kib)}</td>
        <td class="num">${st && !st.new ? st.cpu_percent.toFixed(1) + "%" : `<span class="mute">–</span>`}</td>
        <td class="num opt">${st && memUsed(st) ? kib(memUsed(st)) : `<span class="mute">–</span>`}</td>
        <td class="ops">${rowActions(m)}</td>
      </tr>`;
    }).join("")}</tbody></table></div>
    ${list.length === 0 ? `<p class="mute">${t("没有符合条件的虚拟机")}</p>` : ""}`;
  const byKey = new Map(list.map((m) => [key(m), m]));
  $("#ov-all").onchange = (e) => {
    for (const m of list) e.target.checked ? view.picked.add(key(m)) : view.picked.delete(key(m));
    drawList();
  };
  $("#ov-list").querySelectorAll("th[data-sort]").forEach((h) => {
    h.onclick = () => {
      view.desc = view.sort === h.dataset.sort ? !view.desc : false;
      view.sort = h.dataset.sort;
      drawList();
    };
  });
  $("#ov-list").querySelector("tbody").onclick = (e) => {
    const tr = e.target.closest("tr.open");
    if (!tr) return;
    const m = byKey.get(tr.dataset.k);
    const pick = e.target.closest("[data-pick]");
    if (pick) {
      pick.checked ? view.picked.add(tr.dataset.k) : view.picked.delete(tr.dataset.k);
      drawList();
      return;
    }
    const b = e.target.closest("td.ops [data-a]");
    if (e.target.closest("td.ops") && !b) return;
    if (b) {
      const fn = { delete: app.deleteMachine, xml: app.openXML, console: app.openConsole }[b.dataset.a];
      (fn || ((id, d) => app.power(id, d, b.dataset.a)))(m.connection, m);
      return;
    }
    e.preventDefault();
    app.open(m.connection, m);
  };
}

// bulk does action to the picked machines the list shows, all at once.
async function bulk(action) {
  const list = shownMachines().filter((m) => view.picked.has(key(m)));
  if (!list.length) return;
  const names = list.map((m) => m.name).join(", ");
  if (action === "force-off" && !(await confirmDanger(t("强制关机"), t("强制关闭 {n} 台虚拟机 ({names})? 相当于拔掉电源, 未保存的数据会丢失。", { n: list.length, names })))) return;
  try {
    const rs = await call("PowerMany", list.map((m) => ({ connection: m.connection, uuid: m.uuid })), action);
    const failed = rs.filter((r) => r.error);
    const name = (r) => list.find((m) => m.connection === r.connection && m.uuid === r.uuid)?.name || r.uuid;
    if (failed.length) toast([t("{ok} 台完成, {bad} 台失败:", { ok: rs.length - failed.length, bad: failed.length }), ...failed.map((r) => `· ${name(r)}: ${r.error}`)].join("\n"), true);
    else toast(t("{action}: {n} 台已完成", { action: t(action), n: rs.length }));
  } catch (e) { fail(e); }
  reloadOverview();
}

// Live CPU and memory: the list's machines and the hosts' cards, every
// 2 s while the overview shows, one call per host.
const connected = () => app?.shown() && data ? data.hosts.filter((h) => h.status === "connected").map((h) => h.id) : null;
watch({
  kind: "Stats",
  conn: () => {
    const ids = connected();
    return ids && ids.filter((id) => data.machines.some((m) => m.connection === id && m.state === "running"));
  },
  show: () => { if (app.shown() && !document.querySelector("#ov-list input:focus") && !menuOpen()) drawList(); },
});
watch({
  kind: "HostStats",
  conn: connected,
  show: (u, err, id) => {
    const h = data?.hosts.find((x) => x.id === id);
    if (err || !h || !app.shown()) return;
    h.usage = u;
    drawHosts();
  },
});
