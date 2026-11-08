import { call, on, web } from "./rpc.js";
import { t, translate, setLang } from "./i18n.js";
import { $, esc, toast, fail, kib, gib, confirmDanger, icon, errorCard, emptyState, skeleton } from "./util.js";
import { initMachine, openMachine, closeMachine, shownMachine, loadMachine, renderMachine, showTab } from "./machine.js";
import "./machine-config.js";
import "./machine-disks.js";
import "./machine-nics.js";
import "./machine-devices.js";
import "./machine-snapshots.js";
import "./machine-agent.js";
import "./machine-monitor.js";
import { watch, lastStats, memUsed } from "./monitor.js";
import { thumbsOn, setThumbs, thumbCell, thumbsToggle, watchThumbs, thumbsNow, onThumbsSlow, thumbsNever } from "./thumbs.js";
import { loadSettings, notify } from "./settings.js";
import { onOpDone } from "./ops.js";
import { renderHost } from "./host.js";
import { renderStorage } from "./storage.js";
import { renderNetworks } from "./networks.js";
import { editXML } from "./xml.js";
import { initOverview, renderOverview, reloadOverview } from "./overview.js";
import { initActivity, closeActivity } from "./activity.js";
import { initCreate, openCreate, uploaded } from "./create.js";
import { sideButton, closeDrawer, tabBar, wireTabs } from "./layout.js";
import { initConsole, openConsole, renderConsole, closeConsole, shownConsole, solo } from "./console.js";
import { moreMenu, rowActions } from "./menu.js";
import { parse, format } from "./route.js";
import "./a11y.js";
import { initKeys } from "./keys.js";

// conns: the saved connections, by id, as the last event or list showed them.
// domains: the machines of each connection that has been listed.
// tabs: the view each connection shows (machines, storage, networks, host).
// all: the overview of every host is shown instead of the selected one.
// q: the search in each connection's list of machines.
const state = { conns: new Map(), order: [], sel: null, domains: new Map(), tabs: new Map(), env: null, all: false, q: new Map() };
if (solo) document.body.classList.add("solo");

const statusText = (c) => c.host_key ? t("待确认主机密钥") : ({
  connected: t("已连接"), connecting: t("连接中…"), disconnected: t("已断开"), idle: t("未连接"), closed: t("未连接"),
}[c.status] || c.status);

// ---- connections ----------------------------------------------------------

function setConns(list) {
  state.order = list.map((c) => c.id);
  state.conns = new Map(list.map((c) => [c.id, c]));
  if (!state.conns.has(state.sel)) state.sel = state.order[0] ?? null;
}

function renderSide() {
  $("#go-all").classList.toggle("sel", state.all);
  if (state.all) $("#go-all").setAttribute("aria-current", "page");
  else $("#go-all").removeAttribute("aria-current");
  const had = document.activeElement?.closest?.("#conns .conn")?.dataset.id;
  $("#conns").innerHTML = state.order.map((id) => {
    const c = state.conns.get(id);
    const on = id === state.sel && !state.all;
    return `<div class="conn${on ? " sel" : ""}" data-id="${esc(id)}" role="button" tabindex="0"${on ? ` aria-current="page"` : ""}>
      <span class="dot ${c.host_key ? "pending" : esc(c.status)}"></span>
      <div class="txt"><div class="name">${esc(c.name)}</div><small>${esc(statusText(c))}</small></div>
    </div>`;
  }).join("");
  // Redrawn under the keyboard: it stays on the same connection.
  if (had) $(`#conns .conn[data-id="${CSS.escape(had)}"]`)?.focus();
}

$("#conns").addEventListener("click", (e) => {
  const el = e.target.closest(".conn");
  if (!el) return;
  closeDrawer();
  select(el.dataset.id);
});

$("#go-all").addEventListener("click", () => { closeDrawer(); showAll(); });

function showAll() {
  if (shownConsole()) closeConsole();
  if (shownMachine()) closeMachine();
  state.all = true;
  renderSide();
  renderMain();
  reloadOverview();
}

function select(id) {
  if (shownConsole() && shownConsole().conn !== id) closeConsole();
  if (shownMachine() && shownMachine().conn !== id) closeMachine();
  state.all = false;
  state.sel = id;
  renderSide();
  renderMain();
  const c = state.conns.get(id);
  if (c && c.status === "idle") connect(id);
  else if (c && c.status === "connected" && !state.domains.has(id)) loadDomains(id);
}

async function connect(id) {
  try { await call("Connect", id); } catch (e) { fail(e); }
}

// ---- machines -------------------------------------------------------------

const loading = new Map(); // connection id -> timer, to coalesce bursts of events

function loadDomainsSoon(id) {
  clearTimeout(loading.get(id));
  loading.set(id, setTimeout(() => loadDomains(id), 120));
}

async function loadDomains(id) {
  try {
    state.domains.set(id, await call("Domains", id));
  } catch (e) {
    if (state.conns.get(id)?.status === "connected") fail(e);
  }
  if (reach(id)) return;
  if (id === state.sel) renderMain();
}

async function power(id, d, action) {
  if (action === "force-off" && !(await confirmDanger(t("强制关机"), t("强制关闭 {name}? 相当于拔掉电源, 未保存的数据会丢失。", { name: d.name })))) return;
  try {
    await call("Power", id, d.uuid, action);
  } catch (e) { fail(e); }
  loadDomainsSoon(id); // the lifecycle event also triggers this; a refused action doesn't
}

// ---- where the page is ----------------------------------------------------------

// pending is a machine's page or console the page is to show (from the
// URL: a start, Back or Forward) once its connection's machines are
// listed: { conn, machine, tab } or { conn, console, kind }.
let pending = null;
// replaceNext: the next view drawn takes the place of the URL's instead
// of adding to the history (the URL named it, or something it couldn't
// reach).
let replaceNext = true;
let remembered = "", rememberTimer = null;

// where is the view shown, as route.js reads and writes it.
function where() {
  if (state.all) return { all: true };
  const k = shownConsole();
  if (k) return { conn: k.conn, console: k.name, kind: k.kind };
  const m = shownMachine();
  if (m) return { conn: m.conn, machine: m.name, tab: m.tab };
  return state.sel ? { conn: state.sel, tab: state.tabs.get(state.sel) || "machines" } : null;
}

// titleOf is what the window (or the browser's tab) is called on view v.
function titleOf(v) {
  if (!v) return "virt-meow";
  if (v.all) return `${t("所有主机")} — virt-meow`;
  const conn = state.conns.get(v.conn)?.name || v.conn;
  if (v.console) return solo ? v.console : `${v.console} — ${t("控制台")} — virt-meow`;
  if (v.machine) return `${v.machine} — ${conn} — virt-meow`;
  return `${conn} — virt-meow`;
}

let titled = "";
function syncTitle() {
  const title = titleOf(where());
  document.title = title;
  if (web || title === titled) return;
  titled = title;
  call("SetTitle", title).catch(() => {});
}

// syncURL makes the URL say where the page is, so Back returns here; the
// window also keeps it for its next start. The title follows.
function syncURL() {
  syncTitle();
  if (pending) return; // on the way to what the URL says
  const h = format(where());
  if (!h || h === location.hash) { replaceNext = false; return; }
  history[replaceNext ? "replaceState" : "pushState"](null, "", h);
  replaceNext = false;
  if (!web && !solo && h !== remembered) {
    clearTimeout(rememberTimer);
    rememberTimer = setTimeout(() => { remembered = h; call("RememberView", h.slice(1)).catch(() => {}); }, 500);
  }
}

// go shows the view v (route.js's), from the URL.
function go(v) {
  replaceNext = true;
  if (!v) return false;
  if (v.all) { showAll(); return true; }
  const id = v.conn || state.sel;
  if (!state.conns.has(id)) {
    if (v.conn) toast(t("没有这个连接: {id}", { id: v.conn }), true);
    return false;
  }
  if (v.console || v.machine) {
    pending = { ...v, conn: id };
    if (!reach(id)) select(id); // connects, or lists its machines, and reach follows
    return true;
  }
  if (shownConsole()) closeConsole();
  if (shownMachine()) closeMachine();
  if (v.tab) state.tabs.set(id, v.tab);
  select(id);
  return true;
}

// reach shows what is pending on connection id once its machines are
// listed; true when it did (or found the machine gone).
function reach(id) {
  const p = pending, doms = state.domains.get(id);
  if (!p || p.conn !== id || !doms) return false;
  pending = null;
  replaceNext = true;
  const name = p.console || p.machine, d = doms.find((x) => x.name === name);
  if (!d) {
    toast(t("没有名为 {name} 的虚拟机", { name }), true);
    renderMain();
    return true;
  }
  if (p.console) {
    if (shownConsole()?.uuid !== d.uuid) openConsole(id, d, p.kind);
    return true;
  }
  if (shownConsole()) closeConsole();
  if (shownMachine()?.uuid === d.uuid) showTab(p.tab);
  else openMachine(id, d, p.tab);
  return true;
}

addEventListener("popstate", () => go(parse(location.hash)));

// ---- main pane ------------------------------------------------------------

function renderMain() {
  // A redraw keeps the field being typed in (the list's search).
  const f = document.activeElement, id = f?.id && $("#main").contains(f) ? f.id : "";
  const at = id && "selectionStart" in f ? [f.selectionStart, f.selectionEnd] : null;
  drawMain();
  syncURL();
  const g = id && document.getElementById(id);
  if (g && g !== f) {
    g.focus();
    if (at) try { g.setSelectionRange(...at); } catch { /* not a text field */ }
  }
}

function drawMain() {
  const main = $("#main");
  if (state.all) {
    main.classList.remove("console");
    return renderOverview(main);
  }
  const c = state.conns.get(state.sel);
  if (shownConsole() && c && shownConsole().conn === c.id) {
    // Redrawing would drop the client and its connection: draw once.
    if (main.dataset.view !== "console") renderConsole(main);
    return;
  }
  if (solo) { // a console's own window, before the console is up
    main.innerHTML = c?.error ? errorCard(t("连接失败"), c.error)
      : emptyState({ icon: "monitor", title: t("正在打开控制台…") });
    return;
  }
  main.classList.remove("console");
  if (shownMachine() && c && shownMachine().conn === c.id) {
    renderMachine(main);
    return;
  }
  main.dataset.view = "list";
  if (!c) {
    main.innerHTML = `<div class="head">${sideButton()}</div>` + emptyState({ icon: "server", title: t("还没有连接"),
      text: t("添加一台通过 SSH 访问的主机, 或本机的 libvirt。"), button: { id: "empty-add", label: t("添加连接"), icon: "plus" } });
    $("#empty-add").onclick = openAdd;
    return;
  }
  const canReconnect = c.status !== "connecting";
  main.innerHTML = `<div class="head">
      <div class="lead">${sideButton()}<div><h1>${esc(c.name)}</h1>
        <div class="meta"><span class="mono">${esc(c.uri)}</span><span>${esc(statusText(c))}</span></div></div></div>
      <div class="actions" id="conn-acts">
        ${c.status === "connected" ? `<button class="primary" id="create">${icon("plus")}${t("新建虚拟机")}</button>`
          : `<button data-a="reconnect" ${canReconnect ? "" : "disabled"}>${icon("refresh-cw")}${t("重新连接")}</button>`}
        ${moreMenu([...(c.status === "connected" ? [{ a: "reconnect", label: t("重新连接"), icon: "refresh-cw" }, "-"] : []),
          ...(c.remote ? [{ a: "forget", label: t("忘记记住的密码") }] : []), { a: "remove", label: t("移除连接"), cls: "del" }])}
      </div>
    </div>
    <div id="body"></div>`;
  $("#conn-acts").querySelectorAll("[data-a]").forEach((b) => {
    b.onclick = () => ({ remove: removeConn, forget: forgetSecrets }[b.dataset.a] || ((c) => connect(c.id)))(c);
  });
  if ($("#create")) $("#create").onclick = () => openCreate(c.id);
  const body = $("#body");

  if (c.host_key) {
    body.innerHTML = `<div class="card note"><h3>${icon("triangle-alert")}${t("首次连接这台主机")}</h3>
      <p>${esc(t("{addr} 提供的 {type} 主机密钥是:", { addr: c.host_key.addr, type: c.host_key.type }))}</p>
      <div class="fp">${esc(c.host_key.fingerprint)}</div>
      <p class="mute">${t("请通过其他途径 (例如在主机上运行 ssh-keygen -lf /etc/ssh/ssh_host_*_key.pub) 核对指纹, 一致再信任。")}</p>
      <div class="buttons"><button class="primary" id="trust">${icon("circle-check")}${t("信任并连接")}</button></div></div>`;
    $("#trust").onclick = async () => {
      try { await call("TrustHostKey", c.id, c.host_key.fingerprint); } catch (e) { fail(e); }
    };
    return;
  }
  if (c.host_key_changed) {
    const host = (c.error.match(/host key of (\S+)/) || [])[1] || "";
    body.innerHTML = `<div class="card err" role="alert"><h3>${icon("circle-alert")}${t("主机密钥已变化")}</h3>
      <p>${t("这台主机提供的密钥与记录的不同。可能是主机重装了, 也可能有人在拦截连接。确认原因后, 在终端里移除旧记录再重新连接:")}</p>
      <div class="fp">virt-mm hostkey forget ${esc(host)}</div>
      <p class="msg">${esc(c.error)}</p></div>`;
    return;
  }
  if (c.status === "disconnected" && c.error) {
    body.innerHTML = errorCard(t("连接失败"), c.error);
  }
  if (c.status === "connecting") body.insertAdjacentHTML("beforeend", skeleton());
  if (c.status !== "connected") return;
  const tab = state.tabs.get(c.id) || "machines";
  body.insertAdjacentHTML("beforeend", tabBar("tabs", [
    ["machines", t("虚拟机"), "monitor"], ["storage", t("存储"), "hard-drive"], ["networks", t("网络"), "network"], ["host", t("主机"), "server"],
  ], tab));
  wireTabs($("#tabs"), (k) => { state.tabs.set(c.id, k); renderMain(); });
  if (tab === "host") return renderHost(body, c.id);
  if (tab === "storage") return renderStorage(body, c.id);
  if (tab === "networks") return renderNetworks(body, c.id);
  const doms = state.domains.get(c.id);
  if (!doms) return body.insertAdjacentHTML("beforeend", skeleton(true));
  if (doms.length === 0) {
    body.insertAdjacentHTML("beforeend", emptyState({ icon: "monitor", title: t("这台主机上没有虚拟机"),
      text: t("新建一台: 从安装 ISO、云镜像或已有的磁盘。"), button: { id: "empty-create", label: t("新建虚拟机"), icon: "plus" } }));
    $("#empty-create").onclick = () => openCreate(c.id);
    return;
  }
  const running = doms.filter((d) => d.state === "running").length;
  const pics = running > 0 && thumbsOn(c.id, c.remote, running);
  const q = state.q.get(c.id) || "";
  body.insertAdjacentHTML("beforeend", `<div class="list-opts"><input type="search" id="list-q" placeholder="${esc(t("搜索名称"))}" value="${esc(q)}" spellcheck="false" aria-label="${esc(t("搜索名称"))}">
    ${running > 0 && !thumbsNever() ? thumbsToggle(c.id, pics) : ""}</div>`);
  body.insertAdjacentHTML("beforeend", `<div class="tablewrap"><table class="machines${pics ? " pics" : ""}"><thead><tr>
      ${pics ? `<th class="thumb"></th>` : ""}<th>${t("名称")}</th><th>${t("状态")}</th><th class="opt2">vCPU</th><th class="opt2">${t("内存")}</th><th>CPU</th><th class="opt">${t("内存使用")}</th><th class="opt">${t("自动启动")}</th><th></th>
    </tr></thead><tbody>${doms.map((d, i) => `<tr class="open" data-i="${i}"${matches(d, q) ? "" : " hidden"}>
      ${pics ? thumbCell(c.id, d) : ""}<td><a class="name" href="${esc(format({ conn: c.id, machine: d.name }))}" data-i="${i}">${esc(d.name)}</a>${d.persistent ? "" : `<small>${t("临时")}</small>`}</td>
      <td><span class="badge ${esc(d.state)}">${esc(t(d.state))}</span>${d.saved ? `<small>${t("已保存")}</small>` : ""}</td>
      <td class="num opt2">${d.vcpus || "–"}</td>
      <td class="num mem opt2">${kib(d.max_memory_kib)}</td>
      ${usageCells(c.id, d)}
      <td class="opt">${d.autostart ? "✓" : ""}</td>
      <td class="ops">${rowActions(d)}</td>
    </tr>`).join("")}</tbody></table></div>
    <p class="mute" id="list-none"${doms.some((d) => matches(d, q)) ? " hidden" : ""}>${t("没有符合条件的虚拟机")}</p>`);
  $("#list-q").oninput = (e) => {
    const v = e.target.value;
    state.q.set(c.id, v);
    body.querySelectorAll("tbody tr.open").forEach((tr) => { tr.hidden = !matches(doms[+tr.dataset.i], v); });
    $("#list-none").hidden = doms.some((d) => matches(d, v));
  };
  body.querySelectorAll("td.ops [data-a]").forEach((b) => {
    const d = doms[+b.closest("tr").dataset.i];
    b.onclick = () => ({ delete: deleteMachine, xml: openXML, console: openConsole }[b.dataset.a] || ((id, d) => power(id, d, b.dataset.a)))(c.id, d);
  });
  if ($("#thumbs-on")) {
    $("#thumbs-on").onchange = (e) => { setThumbs(c.id, e.target.checked); renderMain(); };
  }
  if (pics) {
    watchThumbs(body, () => {
      const cc = state.conns.get(state.sel);
      return cc && cc.id === c.id && $("#main").dataset.view === "list" && (state.tabs.get(c.id) || "machines") === "machines"
        ? { conn: c.id, remote: cc.remote, doms: state.domains.get(c.id) || [] } : null;
    });
    thumbsNow();
  }
  // A row opens the machine's page; its buttons do what they say.
  body.querySelector("tbody").addEventListener("click", (e) => {
    const tr = e.target.closest("tr.open");
    if (!tr || e.target.closest("td.ops")) return;
    e.preventDefault();
    openMachine(c.id, doms[+tr.dataset.i]);
  });
}

// matches says whether a machine is one the list's search q asks for.
function matches(d, q) {
  q = q.trim().toLowerCase();
  return !q || d.name.toLowerCase().includes(q) || (d.title || "").toLowerCase().includes(q);
}

// Pictures that went off by themselves: the list says so.
onThumbsSlow((conn) => { if (conn === state.sel && $("#main").dataset.view === "list") renderMain(); });

// usageCells are a machine's CPU and memory in use in the list, from the
// last Stats; the list's watcher fills them in every 2 s.
function usageCells(conn, d) {
  const m = d.state === "running" ? lastStats(conn, d.uuid) : null;
  const cpu = m && !m.new ? `${m.cpu_percent.toFixed(1)}%` : "";
  const mem = m && memUsed(m) ? kib(memUsed(m)) : "";
  return `<td class="num" data-cpu="${esc(d.uuid)}">${cpu || `<span class="mute">–</span>`}</td>
      <td class="num opt" data-mem="${esc(d.uuid)}" title="${m && !m.guest_used_kib ? esc(t("客户机没有报告, 这是它在主机上占用的内存")) : ""}">${mem || `<span class="mute">–</span>`}</td>`;
}

watch({
  kind: "Stats",
  conn: () => {
    const c = state.conns.get(state.sel);
    return c && c.status === "connected" && $("#main").dataset.view === "list" && (state.tabs.get(c.id) || "machines") === "machines" &&
      (state.domains.get(c.id) || []).some((d) => d.state === "running") ? c.id : null;
  },
  show: (st, err) => {
    if (err) return;
    const c = state.conns.get(state.sel);
    for (const d of state.domains.get(c?.id) || []) {
      const tr = document.querySelector(`td[data-cpu="${CSS.escape(d.uuid)}"]`)?.parentElement;
      if (!tr) continue;
      tr.querySelectorAll("td[data-cpu], td[data-mem]").forEach((x) => x.remove());
      tr.querySelector("td.mem").insertAdjacentHTML("afterend", usageCells(c.id, d));
    }
  },
});

// forgetSecrets drops what the keychain remembers for c: its hosts'
// passwords and its keys' passphrases, asked again when next needed.
async function forgetSecrets(c) {
  if (!(await confirmDanger(t("忘记记住的密码"), t("忘记钥匙串里为 {name} 记住的 SSH 密码和私钥口令? 下次连接时会再问。私钥的口令也是用同一私钥的其他连接的。", { name: c.name }), t("忘记")))) return;
  try {
    const forgot = await call("ForgetSecrets", c.id);
    toast(forgot.length ? t("已忘记 {n} 项: {what}", { n: forgot.length, what: forgot.map((s) => s.kind === "password" ? `${s.user}@${s.addr}` : s.key).join(", ") })
      : t("没有为它记住的密码"));
  } catch (e) { fail(e); }
}

async function removeConn(c) {
  if (!(await confirmDanger(t("移除连接"), t("移除 {name}? 虚拟机本身不受影响。", { name: c.name }), t("移除")))) return;
  try {
    await call("RemoveConnection", c.id);
    state.domains.delete(c.id);
    setConns(await call("Connections"));
    renderSide();
    renderMain();
  } catch (e) { fail(e); }
}

// ---- dialogs --------------------------------------------------------------

document.querySelectorAll("dialog [data-close]").forEach((b) => { b.onclick = () => b.closest("dialog").close("cancel"); });

let addKind = "ssh";
const form = $("#form-add");

function addURI() {
  const f = form.elements;
  switch (addKind) {
    case "system": return "qemu:///system";
    case "session": return "qemu:///session";
    case "uri": return f.uri.value.trim();
  }
  const host = f.host.value.trim();
  if (!host) return "";
  const user = f.user.value.trim();
  const port = f.port.value.trim();
  const h = host.includes(":") && !host.startsWith("[") ? `[${host}]` : host;
  return `qemu+ssh://${user ? encodeURIComponent(user) + "@" : ""}${h}${port ? ":" + port : ""}/system`;
}

function syncAdd() {
  form.querySelectorAll("#kind button").forEach((b) => b.classList.toggle("on", b.dataset.kind === addKind));
  form.querySelectorAll(".fields").forEach((el) => { el.hidden = el.dataset.for !== addKind; });
  $("#uri-preview").textContent = addURI();
  showRoute(addURI());
}

// What the SSH configuration (of the account virt-meow runs as) makes of an
// SSH URI's host, once it says anything: an alias is seen to resolve, its
// jump hosts named.
let routeTimer = 0, routeFor = "";
function showRoute(uri) {
  clearTimeout(routeTimer);
  routeFor = uri;
  const el = $("#uri-route");
  if (!/^[a-z0-9]+\+(ssh|libssh2?):\/\/[^/]/i.test(uri)) {
    el.textContent = "";
    return;
  }
  routeTimer = setTimeout(async () => {
    let text = "", bad = false;
    try {
      const hops = await call("SSHRoute", uri);
      if (hops.some((h) => h.configured)) text = routeText(hops);
    } catch (e) { text = e.message; bad = true; }
    if (routeFor !== uri) return;
    el.textContent = text;
    el.classList.toggle("err", bad);
  }, 250);
}

function routeText(hops) {
  const login = (h) => `${h.user}@${h.host.includes(":") ? `[${h.host}]` : h.host}${h.port === "22" ? "" : ":" + h.port}`;
  const host = hops[hops.length - 1], via = hops.slice(0, -1);
  return via.length ? t("按 SSH 配置: {login}, 经跳板机 {via}", { login: login(host), via: via.map(login).join(" → ") })
    : t("按 SSH 配置: {login}", { login: login(host) });
}

function openAdd() {
  form.reset();
  addKind = state.env?.os === "linux" && state.order.length === 0 ? "system" : "ssh";
  syncAdd();
  $("#dlg-add").showModal();
}

$("#add").onclick = openAdd;
form.querySelector("#kind").addEventListener("click", (e) => {
  const b = e.target.closest("button");
  if (!b) return;
  addKind = b.dataset.kind;
  syncAdd();
});
form.addEventListener("input", syncAdd);
form.addEventListener("submit", async (e) => {
  e.preventDefault();
  const uri = addURI();
  if (!uri) return;
  try {
    const c = await call("AddConnection", form.elements.name.value, uri, form.elements.autoconnect.checked);
    $("#dlg-add").close();
    setConns(await call("Connections"));
    select(c.id);
  } catch (err) { fail(err); }
});

// A secret the SSH handshake needs: answered or cancelled, never left hanging.
const prompts = [];
function nextPrompt() {
  const dlg = $("#dlg-prompt");
  if (dlg.open || prompts.length === 0) return;
  const p = prompts[0];
  const f = $("#form-prompt");
  f.reset();
  if (p.kind === "password") {
    $("#prompt-title").textContent = t("需要密码");
    $("#prompt-text").textContent = t("{user}@{addr} 的密码", { user: p.user, addr: p.addr });
  } else {
    $("#prompt-title").textContent = t("需要私钥口令");
    $("#prompt-text").textContent = t("{key} 的口令", { key: p.key });
  }
  $("#prompt-refused").hidden = !p.refused;
  $("#prompt-refused").textContent = p.kind === "password" ? t("记住的密码被拒绝了, 已从钥匙串里忘掉。") : t("记住的口令打不开这个私钥了, 已从钥匙串里忘掉。");
  $("#prompt-remember").hidden = !p.can_remember;
  dlg.returnValue = "";
  dlg.showModal();
  f.elements.secret.focus();
}
$("#form-prompt").addEventListener("submit", (e) => {
  e.preventDefault();
  const p = prompts.shift();
  const f = e.target.elements;
  call("Answer", p.id, f.secret.value, true, !!p.can_remember && f.remember.checked);
  $("#dlg-prompt").close("ok");
});
$("#dlg-prompt").addEventListener("close", () => {
  if ($("#dlg-prompt").returnValue === "cancel" || $("#dlg-prompt").returnValue === "") {
    const p = prompts.shift();
    if (p) call("Answer", p.id, "", false, false);
  }
  nextPrompt();
});

// ---- delete ---------------------------------------------------------------

async function deleteMachine(id, d) {
  const dlg = $("#dlg-delete");
  $("#delete-title").textContent = t("删除虚拟机");
  $("#delete-text").textContent = t("删除 {name}? 正在运行会先被强制关机, 定义、NVRAM 和 TPM 状态都会删除。", { name: d.name });
  const box = dlg.querySelector("[name=disks]"), list = $("#delete-disks");
  box.checked = false;
  list.hidden = true;
  list.innerHTML = "";
  box.onchange = () => { list.hidden = !box.checked || !list.children.length; };
  // The disks the box deletes, by name: what is lost.
  call("Devices", id, d.uuid).then((dev) => {
    const disks = (dev.disks || []).filter((x) => x.device === "disk" && x.source);
    list.innerHTML = disks.map((x) => `<li>${esc(x.source)}${x.capacity ? ` <small>${esc(gib(x.capacity))}</small>` : ""}</li>`).join("");
    box.onchange();
  }).catch(() => {});
  dlg.returnValue = "";
  dlg.showModal();
  const ok = await new Promise((r) => dlg.addEventListener("close", () => r(dlg.returnValue === "ok"), { once: true }));
  if (!ok) return false;
  let done = false;
  try {
    const removed = await call("DeleteMachine", id, d.uuid, dlg.querySelector("[name=disks]").checked);
    toast([t("已删除 {name}", { name: d.name }), ...removed].join("\n"));
    done = true;
  } catch (e) { fail(e); }
  loadDomainsSoon(id);
  return done;
}

// ---- XML editor -----------------------------------------------------------

function openXML(id, d) {
  editXML({
    load: () => call("Definition", id, d.uuid),
    save: (def, x) => call("Redefine", id, def.uuid, def.xml, x),
    title: (def) => t("{name} 的 XML", { name: def.name }),
    note: (def) => def.active
      ? t("它正在运行: 这里是下次启动用的定义, 保存的修改在下次启动时生效。")
      : t("它下次启动时用这份定义。"),
    saved: (def) => {
      toast(def.active ? t("已保存 {name}, 下次启动时生效", { name: def.name }) : t("已保存 {name}", { name: def.name }));
      loadDomainsSoon(id);
    },
  }).catch(fail);
}

// ---- uploads --------------------------------------------------------------

function showUpload(u) {
  let el = document.getElementById("up-" + u.id);
  if (!el) {
    el = document.createElement("div");
    el.id = "up-" + u.id;
    el.className = "upload";
    $("#uploads").append(el);
  }
  const pct = u.total ? Math.floor((u.sent / u.total) * 100) : 0;
  if (u.state === "running") {
    el.innerHTML = `<div class="top"><span>${esc(t("正在上传 {name}", { name: u.name }))} — ${pct}%</span>
      <button type="button">${t("取消")}</button></div><div class="bar"><div style="width:${pct}%"></div></div>`;
    el.querySelector("button").onclick = () => call("CancelUpload", u.id);
    return;
  }
  el.remove();
  if (u.state === "done") toast(t("已上传 {name}", { name: u.name }));
  else if (u.state === "cancelled") toast(t("已取消上传 {name}", { name: u.name }));
  else toast(t("上传 {name} 失败: {err}", { name: u.name, err: u.error }), true);
  uploaded(u);
}

on("upload", (u) => showUpload(u));

initKeys({
  newMachine: () => {
    const c = state.conns.get(state.sel);
    if (solo || state.all || !c || c.status !== "connected") return false;
    openCreate(c.id);
    return true;
  },
  // The list's search: a host's machines, or every host's.
  search: () => {
    const el = $(state.all ? "#ov-q" : "#list-q");
    if (!el || solo) return false;
    el.focus();
    el.select();
    return true;
  },
  back: () => {
    if (closeActivity()) return true;
    if (shownConsole() && !solo) { closeConsole(); renderMain(); return true; }
    if (shownMachine()) { closeMachine(); renderMain(); return true; }
    return false;
  },
});

// ---- events ---------------------------------------------------------------

// virt-mm gui create: the form opens on the selected connection once it is up.
let openCreatePending = location.hash === "#create";

on("connection", (c) => {
  const before = state.conns.get(c.id);
  if (openCreatePending && c.id === state.sel && c.status === "connected") {
    openCreatePending = false;
    setTimeout(() => openCreate(c.id), 0);
  }
  state.conns.set(c.id, c);
  if (!state.order.includes(c.id)) state.order.push(c.id);
  if (c.status === "connected" && before?.status !== "connected") loadDomains(c.id);
  if (c.status !== "connected") state.domains.delete(c.id);
  renderSide();
  if (state.all) reloadOverview();
  else if (c.id === state.sel) renderMain();
});

on("domain", (ev) => {
  loadDomainsSoon(ev.connection);
  // A machine that stopped by itself: the desktop hears of it (settings.js
  // sends it only while no virt-meow window has focus).
  const e = ev.event, host = state.conns.get(ev.connection)?.name || "";
  if (e.kind === "crashed" || (e.kind === "stopped" && ["crashed", "failed"].includes(e.detail))) notify(t("{name} 崩溃了", { name: e.name }), host);
  else if (e.kind === "stopped" && e.detail === "shutdown") notify(t("{name} 已关机", { name: e.name }), host);
  if (state.all) reloadOverview();
  if (shownMachine()?.conn === ev.connection) loadMachine();
});

// Under serve, the event stream came back after a break: what changed
// meanwhile wasn't heard, so reload it.
on("virt-mm:reconnected", async () => {
  try {
    setConns(await call("Connections"));
    state.domains.clear();
    renderSide();
    renderMain();
    loadMachine();
    for (const id of state.order) if (state.conns.get(id).status === "connected") loadDomains(id);
  } catch (e) { fail(e); }
});

// A second virt-mm gui handed its view to this window.
on("view", (v) => { if (!solo) go(parse("#" + v)); });

// A long operation's end, for the desktop too.
onOpDone((label, err) => notify(err ? t("没能完成: {what}", { what: label }) : t("已完成: {what}", { what: label }), err ? String(err.message || err) : ""));

on("not-remembered", (n) => {
  const what = n.kind === "password" ? t("{user}@{addr} 的密码", { user: n.user, addr: n.addr }) : t("{key} 的口令", { key: n.key });
  toast(t("没能记住{what}: {err}", { what, err: n.error }), true);
});
on("prompt", (p) => {
  prompts.push(p);
  nextPrompt();
});

// ---- start ----------------------------------------------------------------

initConsole({
  select: (id) => { if (state.sel !== id || state.all) select(id); },
  render: () => renderMain(),
  // Back from a console: to the machine's page when it came from there.
  back: () => renderMain(),
  moved: () => syncURL(),
});

initMachine({
  render: () => renderMain(),
  back: () => { closeMachine(); renderMain(); },
  power, openConsole, openXML, deleteMachine,
  reloadList: (id) => loadDomainsSoon(id),
});

// The overview's machines open where they live: their host is selected.
initOverview({
  power, openXML, deleteMachine,
  openConsole: (id, d) => { select(id); openConsole(id, d); },
  open: (id, d) => { select(id); openMachine(id, d); },
  host: (id) => select(id),
  shown: () => state.all && $("#main").dataset.view === "overview",
});

initCreate({
  reloadList: (id) => loadDomainsSoon(id),
  // Customize before install: the new machine's page, with Start there.
  customize: (id, d) => { if (state.sel === id) openMachine(id, d, "overview", { fresh: true }); },
});

initActivity({
  connName: (id) => state.conns.get(id)?.name || "",
  machineName: (id, uuid) => (state.domains.get(id) || []).find((d) => d.uuid === uuid)?.name || "",
});

state.env = await call("Env");
await loadSettings(() => renderMain());
if (state.env.lang) setLang(state.env.lang); // in a browser, its own language
if (web) {
  // Under serve with a login: a way out.
  fetch("/api/session").then((r) => r.json()).then((s) => {
    if (!s.login) return;
    $("#logout").hidden = false;
    $("#logout").onclick = async () => {
      await fetch("/api/logout", { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" }).catch(() => {});
      location.href = "/login?out";
    };
  }).catch(() => {});
}
translate();
setConns(await call("Connections"));
renderSide();
// Where the URL says (a reload, a second window, virt-mm gui …), else the
// first connection.
if (!go(parse(location.hash))) {
  if (state.sel) select(state.sel);
  else renderMain();
}
if (openCreatePending && state.conns.get(state.sel)?.status === "connected") {
  openCreatePending = false;
  openCreate(state.sel);
}
