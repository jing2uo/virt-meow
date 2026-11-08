// A connection's networks tab: its virtual networks, and the leases and
// fixed addresses of the one picked; what virt-mm net does.
import { call } from "./rpc.js";
import { t } from "./i18n.js";
import { $, esc, fail, errText, toast, formDialog, confirmDanger, showEdited, icon, errorCard, emptyState, skeleton } from "./util.js";
import { editXML } from "./xml.js";
import { moreMenu } from "./menu.js";

// cache: conn -> { nets, sel, leases, hosts, at, err, selErr }, drawn from
// while read again (the page redraws the tab on every machine event).
const FRESH = 3000;
const cache = new Map();
let shown = null;

export function renderNetworks(body, conn) {
  shown = conn;
  const c = cache.get(conn) || { sel: "", at: 0 };
  cache.set(conn, c);
  body.insertAdjacentHTML("beforeend", `<div id="networks">${draw(conn)}</div>`);
  wire(conn);
  if (Date.now() - c.at > FRESH) load(conn);
}

async function load(conn) {
  const c = cache.get(conn);
  c.at = Date.now();
  try {
    c.nets = await call("Networks", conn);
    c.err = "";
  } catch (e) { c.err = errText(e); }
  if (c.nets && !c.nets.some((n) => n.name === c.sel)) c.sel = (c.nets.find((n) => n.active) || c.nets[0] || {}).name || "";
  await loadSel(conn);
}

// loadSel reads the picked network's leases (while it runs) and fixed
// addresses (a bridge network has neither: the LAN hands them out).
async function loadSel(conn) {
  const c = cache.get(conn), n = c.nets?.find((x) => x.name === c.sel);
  c.leases = c.hosts = null;
  c.selErr = "";
  if (n && n.forward !== "bridge") {
    try {
      [c.hosts, c.leases] = await Promise.all([call("DHCPHosts", conn, n.name), n.active ? call("Leases", conn, n.name) : []]);
    } catch (e) { c.selErr = errText(e); }
  }
  redraw(conn);
}

function redraw(conn) {
  if (shown !== conn || !$("#networks")) return;
  // Redrawn under the keyboard: focus stays on the same row's same thing.
  const f = document.activeElement, row = f?.closest?.("#networks tr[data-i]"), table = row?.closest("table")?.className;
  const what = f === row ? "" : f?.dataset?.a || "";
  $("#networks").innerHTML = draw(conn);
  wire(conn);
  if (row) {
    const r = $(`#networks table.${table} tr[data-i="${row.dataset.i}"]`);
    (what && r?.querySelector(`[data-a="${what}"]`) || r)?.focus({ preventScroll: true });
  }
}

const kind = (n) => ({ nat: "NAT", route: t("路由"), "": t("隔离"), bridge: t("桥接"), open: t("开放") })[n.forward] ?? n.forward;

function draw(conn) {
  const c = cache.get(conn);
  const nets = c.err ? errorCard(t("读不到虚拟网络"), c.err)
    : !c.nets ? skeleton(true)
    : !c.nets.length ? emptyState({ small: true, icon: "network", title: t("还没有虚拟网络"), text: t("添加一个 NAT 网络让虚拟机上网, 或桥接到主机已有的网桥。") })
    : `<div class="tablewrap"><table class="nets"><thead><tr>
        <th>${t("名称")}</th><th>${t("类型")}</th><th class="opt">${t("网桥")}</th><th class="opt2">${t("地址")}</th><th class="opt">DHCP</th><th>${t("状态")}</th><th class="opt2">${t("自动启动")}</th><th></th>
      </tr></thead><tbody>${c.nets.map((n, i) => `<tr class="open${n.name === c.sel ? " sel" : ""}" data-i="${i}" tabindex="0" aria-selected="${n.name === c.sel}">
        <td><b>${esc(n.name)}</b></td>
        <td>${esc(kind(n))}${n.dev ? ` <small>${esc(t("经 {dev}", { dev: n.dev }))}</small>` : ""}</td>
        <td class="mono opt">${esc(n.bridge || "–")}</td>
        <td class="mono opt2">${esc(n.address || "–")}</td>
        <td class="mono opt">${esc(n.dhcp || "–")}</td>
        <td><span class="badge ${n.active ? "running" : "shutoff"}">${n.active ? t("已启动") : t("未启动")}</span></td>
        <td class="opt2">${n.autostart ? "✓" : ""}</td>
        <td class="ops"><div>${n.active ? `<button data-i="${i}" data-a="stop">${icon("power")}${t("停止")}</button>` : `<button data-i="${i}" data-a="start">${icon("play")}${t("启动")}</button>`}${moreMenu([
          { a: "autostart", label: n.autostart ? t("关闭自动启动") : t("自动启动"), data: { i } },
          { a: "xml", label: t("编辑 XML"), data: { i } }, "-",
          { a: "remove", label: t("移除"), cls: "del", data: { i } }])}</div></td>
      </tr>`).join("")}</tbody></table></div>`;
  return `<section><h2>${t("虚拟网络")}</h2>
      <div class="toolbar"><button id="n-add">${icon("plus")}${t("添加网络…")}</button><button id="n-reload">${icon("refresh-cw")}${t("刷新")}</button></div>
      ${nets}</section>${selected(c)}`;
}

function selected(c) {
  const n = c.nets?.find((x) => x.name === c.sel);
  if (!n) return "";
  if (n.forward === "bridge") {
    return `<section><h2>${esc(n.name)}</h2><p class="mute">${esc(t("{name} 把客户机接到主机的网桥 {br} 上: 地址由那个网络的 DHCP 分配, 不归 libvirt 管。", { name: n.name, br: n.bridge }))}</p></section>`;
  }
  if (c.selErr) return `<section><h2>${esc(n.name)}</h2>${errorCard(t("读不到租约和固定地址"), c.selErr)}</section>`;
  if (!c.hosts) return `<section><h2>${esc(n.name)}</h2>${skeleton(true)}</section>`;
  const leases = !n.active ? `<p class="mute">${t("网络启动后才有租约。")}</p>`
    : !c.leases.length ? `<p class="mute">${t("还没有租约")}</p>`
    : `<div class="tablewrap"><table><thead><tr><th>MAC</th><th>${t("地址")}</th><th class="opt2">${t("主机名")}</th><th>${t("虚拟机")}</th><th class="opt">${t("到期")}</th><th></th></tr></thead>
      <tbody>${c.leases.map((l, i) => `<tr>
        <td class="mono">${esc(l.mac)}</td><td class="mono">${esc(l.ip)}</td><td class="opt2">${esc(l.hostname || "–")}</td><td>${esc(l.machine || "–")}</td>
        <td class="num opt">${l.expires ? esc(new Date(l.expires).toLocaleString()) : t("不过期")}</td>
        <td class="ops"><div>${c.hosts.some((h) => h.mac.toLowerCase() === l.mac.toLowerCase()) ? "" : `<button data-l="${i}">${t("固定这个地址")}</button>`}</div></td>
      </tr>`).join("")}</tbody></table></div>`;
  const hosts = !c.hosts.length ? `<p class="mute">${t("没有固定地址")}</p>`
    : `<div class="tablewrap"><table><thead><tr><th>MAC</th><th>${t("地址")}</th><th class="opt2">${t("主机名")}</th><th class="opt">${t("生效")}</th><th></th></tr></thead>
      <tbody>${c.hosts.map((h, i) => `<tr>
        <td class="mono">${esc(h.mac)}</td><td class="mono">${esc(h.ip)}</td><td class="opt2">${esc(h.name || "–")}</td>
        <td class="opt">${h.live && h.defined ? t("现在和重启后") : h.live ? t("只在现在 (重启网络后消失)") : t("网络重启后")}</td>
        <td class="ops"><div><button class="del" data-h="${i}">${t("移除")}</button></div></td>
      </tr>`).join("")}</tbody></table></div>`;
  return `<section><h2>${esc(t("{name} 的 DHCP 租约", { name: n.name }))}</h2>${leases}</section>
    <section><h2>${esc(t("{name} 的固定地址", { name: n.name }))}</h2>
      <div class="toolbar"><button id="h-add">${icon("plus")}${t("添加固定地址…")}</button></div>${hosts}
      <p class="hint">${t("固定地址让 DHCP 总给这个 MAC 同一个地址; 客户机下次续租时拿到。")}</p></section>`;
}

function wire(conn) {
  const el = $("#networks"), c = cache.get(conn);
  if (!el) return;
  $("#n-add").onclick = () => addNetwork(conn);
  $("#n-reload").onclick = () => load(conn);
  el.querySelectorAll("table.nets tr.open").forEach((tr) => {
    tr.onclick = (e) => {
      if (e.target.closest("button")) return;
      const n = c.nets[+tr.dataset.i];
      if (n.name === c.sel) return;
      c.sel = n.name;
      loadSel(conn);
      redraw(conn);
    };
  });
  el.querySelectorAll("table.nets td.ops [data-a]").forEach((b) => {
    const n = c.nets[+b.dataset.i];
    b.onclick = () => act(conn, n, b.dataset.a);
  });
  const n = c.nets?.find((x) => x.name === c.sel);
  if ($("#h-add")) $("#h-add").onclick = () => addHost(conn, n, {});
  el.querySelectorAll("button[data-l]").forEach((b) => {
    const l = c.leases[+b.dataset.l];
    b.onclick = () => addHost(conn, n, { mac: l.mac, ip: l.ip.split("/")[0], name: l.hostname || l.machine || "" });
  });
  el.querySelectorAll("button[data-h]").forEach((b) => {
    const h = c.hosts[+b.dataset.h];
    b.onclick = async () => {
      if (!(await confirmDanger(t("移除固定地址"), t("不再固定给 {mac} 地址 {ip}?", { mac: h.mac, ip: h.ip }), t("移除")))) return;
      try { showEdited(await call("RemoveDHCPHost", conn, n.name, h.mac)); } catch (e) { fail(e); }
      loadSel(conn);
    };
  });
}

// inUse: hv.ErrNetworkInUse, whose text starts the error.
const inUse = (e) => errText(e).startsWith("network in use");

async function act(conn, n, a) {
  if (a === "xml") return openXML(conn, n);
  if (a === "remove") {
    if (!(await confirmDanger(t("移除网络"), t("移除虚拟网络 {name}? 它的定义会删掉; 接在它上面的虚拟机的网卡留着, 但在它重新添加前连不上网。", { name: n.name }), t("移除")))) return;
    return forced(conn, n, (force) => call("RemoveNetwork", conn, n.name, force), t("已移除网络 {name}", { name: n.name }));
  }
  const action = { start: "start", stop: "stop", autostart: n.autostart ? "autostart-off" : "autostart-on" }[a];
  forced(conn, n, (force) => call("SetNetwork", conn, n.name, action, force));
}

// forced runs fn without force, and when running machines are on the
// network, asks and runs it again with force.
async function forced(conn, n, fn, done = "") {
  try {
    await fn(false);
    if (done) toast(done);
  } catch (e) {
    if (!inUse(e)) fail(e);
    else if (await confirmDanger(t("网络正在使用"), errText(e).replace(/^network in use: /, "").replace(/ \(--force[^)]*\)/, "") + "\n\n" + t("仍然停止? 这些虚拟机会断网, 网络恢复后也要重启它们 (或重新接上网卡) 才能再连上。"), t("仍然停止"))) {
      try { await fn(true); if (done) toast(done); } catch (e2) { fail(e2); }
    }
  }
  load(conn);
}

function openXML(conn, n) {
  editXML({
    load: () => call("NetworkDefinition", conn, n.name),
    save: (def, x) => call("RedefineNetwork", conn, def.name, def.xml, x),
    title: (def) => t("网络 {name} 的 XML", { name: def.name }),
    note: (def) => def.active ? t("它正在运行: 保存的修改在网络重启后生效。") : t("它下次启动时用这份定义。"),
    saved: (def) => {
      toast(def.active ? t("已保存 {name}, 网络重启后生效", { name: def.name }) : t("已保存 {name}", { name: def.name }));
      load(conn);
    },
  }).catch(fail);
}

async function addNetwork(conn) {
  let brs = [];
  try { brs = await call("Bridges", conn); } catch { /* none to offer; typing one still works */ }
  formDialog({
    title: t("添加虚拟网络"),
    body: `<label><span>${t("名称")}</span><input name="name" required spellcheck="false" autocomplete="off"></label>
      <div class="radio"><label><input type="radio" name="type" value="nat" checked>NAT</label>
        <label><input type="radio" name="type" value="route">${t("路由")}</label>
        <label><input type="radio" name="type" value="isolated">${t("隔离")}</label>
        <label><input type="radio" name="type" value="bridge">${t("桥接")}</label></div>
      <p class="hint" data-type="nat">${t("客户机经主机的路由上网, 地址被主机伪装; 外面连不进来。")}</p>
      <p class="hint" data-type="route">${t("和 NAT 一样经主机出去, 但不伪装: 局域网要有回到这个子网的路由。")}</p>
      <p class="hint" data-type="isolated">${t("客户机只能互相访问和访问主机。")}</p>
      <p class="hint" data-type="bridge">${t("客户机直接接到主机已有的网桥上, 和主机在同一个局域网。")}</p>
      <div class="row" data-type="nat route isolated">
        <label><span>${t("主机在网络上的地址 (可选)")}</span><input name="address" spellcheck="false" placeholder="192.168.100.1/24"></label>
        <label><span>${t("DHCP 范围 (可选)")}</span><input name="dhcp" spellcheck="false" placeholder="192.168.100.2-192.168.100.254"></label></div>
      <label class="check" data-type="nat route isolated"><input type="checkbox" name="nodhcp">${t("不要 DHCP")}</label>
      <label data-type="nat route"><span>${t("只经这个主机接口出去 (可选)")}</span><input name="dev" spellcheck="false" placeholder="eth0"></label>
      <label data-type="bridge"><span>${t("主机上的网桥")}</span>${brs.length
        ? `<select name="bridge">${brs.map((b) => `<option>${esc(b)}</option>`).join("")}</select>`
        : `<input name="bridge" spellcheck="false" placeholder="br0">`}</label>
      <p class="hint" data-type="nat route isolated">${t("子网留空时选一个没被占用的 192.168.x.0/24; 主机取它的 .1。")}</p>
      <label class="check"><input type="checkbox" name="autostart" checked>${t("随主机启动")}</label>`,
    ok: t("添加"),
    busy: t("添加中…"),
    submit: async (f) => {
      const type = f.type.value, nn = { name: f.name.value.trim(), type, autostart: f.autostart.checked };
      if (type === "bridge") nn.bridge = f.bridge.value.trim();
      else {
        if (f.address.value.trim()) nn.address = f.address.value.trim();
        if (f.nodhcp.checked) nn.no_dhcp = true;
        else if (f.dhcp.value.trim()) nn.dhcp = f.dhcp.value.trim();
        if (type !== "isolated" && f.dev.value.trim()) nn.dev = f.dev.value.trim();
      }
      const n = await call("AddNetwork", conn, nn);
      toast(t("已添加网络 {name}", { name: n.name }));
      cache.get(conn).sel = n.name;
      load(conn);
    },
  });
  const f = $("#form-dialog");
  const show = () => {
    f.querySelectorAll("[data-type]").forEach((el) => { el.hidden = !el.dataset.type.split(" ").includes(f.elements.type.value); });
    f.elements.dhcp.disabled = f.elements.nodhcp.checked;
  };
  f.querySelectorAll("[name=type]").forEach((r) => { r.onchange = show; });
  f.elements.nodhcp.onchange = show;
  show();
}

function addHost(conn, n, v) {
  formDialog({
    title: t("{name} 的固定地址", { name: n.name }),
    body: `<label><span>MAC</span><input name="mac" required spellcheck="false" placeholder="52:54:00:…" value="${esc(v.mac || "")}"></label>
      <div class="row"><label><span>${t("地址")}</span><input name="ip" required spellcheck="false" placeholder="${esc((n.address || "").replace(/\.\d+\/\d+$/, ".50"))}" value="${esc(v.ip || "")}"></label>
        <label><span>${t("主机名 (可选)")}</span><input name="name" spellcheck="false" value="${esc(v.name || "")}"></label></div>
      <p class="hint">${n.active ? t("运行中的网络立即生效, 也写进定义。") : t("写进定义, 网络启动后生效。")}</p>`,
    ok: t("添加"),
    submit: async (f) => {
      showEdited(await call("AddDHCPHost", conn, n.name, f.mac.value.trim(), f.ip.value.trim(), f.name.value.trim()));
      loadSel(conn);
    },
  });
}
