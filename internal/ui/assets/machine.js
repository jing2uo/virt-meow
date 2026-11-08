// A machine's details page: what virt-mm info shows, its power actions,
// autostart, title and description, rename, clone, delete, console and
// XML. It fills the main pane, as the console does, with Back to the list.
import { call } from "./rpc.js";
import { t } from "./i18n.js";
import { $, esc, toast, fail, errText, kib, gib, confirmDanger, formDialog, showEdited, icon, errorCard, skeleton, emptyState } from "./util.js";
import { sideButton, tabBar, wireTabs } from "./layout.js";
import { moreMenu, machineActions, actionButton } from "./menu.js";
import { runOp, busy, onOps } from "./ops.js";

// cur is the machine shown: its connection and UUID, its name, the info
// last read (api.MachineInfo) or the error reading it, and its tab.
let cur = null;
// app is what the rest of the page does for this one: redraw the main
// pane, and the list's actions on a machine (power, console, XML, delete).
let app = null;

export function initMachine(hooks) { app = hooks; }

// opKey names a machine's long operations (ops.js): one at a time.
export const opKey = (conn, uuid) => `m:${conn}/${uuid}`;

// One starting or ending on the machine shown changes what its page offers.
onOps(() => { if (cur) app.render(); });

export const shownMachine = () => cur;

// The page's tabs, in order: { id, label, html(k), wire(k, body) }. html
// draws the tab from k.info; wire gives it its handlers. Other modules add
// theirs (machine-config.js …).
const tabs = [];
export function addTab(tab) { tabs.push(tab); }

// fresh: a machine just made to be customized before it is installed; the
// page says so, with Start, until it runs.
export function openMachine(conn, d, tab = "overview", { fresh = false } = {}) {
  cur = { conn, uuid: d.uuid, name: d.name, info: null, error: "", tab, fresh };
  app.render();
  loadMachine();
}

export function closeMachine() { cur = null; }

// showTab shows another tab of the machine shown (Back, Forward).
export function showTab(tab) {
  if (!cur || cur.tab === tab) return;
  cur.tab = tabs.some((x) => x.id === tab) ? tab : "overview";
  app.render();
}

// loadMachine reads the shown machine again; after an event on its
// connection, or an action here.
export async function loadMachine() {
  const k = cur;
  if (!k) return;
  try {
    k.info = await call("Info", k.conn, k.uuid);
    k.name = k.info.name;
    k.error = "";
  } catch (e) {
    k.error = errText(e);
    if (/domain not found|no domain/i.test(k.error)) k.info = null; // gone: nothing left to show of it
  }
  if (cur === k) app.render();
}

export const off = (st) => st === "shutoff" || st === "crashed";

// dirty marks a form in a tab as edited: reloading the machine then
// redraws the page around it, not the form, until it is saved or the tab
// left.
export function trackDirty(form) {
  form.addEventListener("input", () => form.classList.add("dirty"));
  form.addEventListener("change", () => form.classList.add("dirty"));
}

// ---- drawing ----------------------------------------------------------------

export function renderMachine(main) {
  const k = cur, m = k.info;
  const keep = m && main.dataset.view === "machine" && main.dataset.uuid === k.uuid && main.dataset.tab === k.tab && $("#m-body form.dirty");
  const body = keep ? $("#m-body") : null;
  main.dataset.view = "machine";
  main.dataset.uuid = k.uuid;
  main.dataset.tab = k.tab;
  const isOff = m ? off(m.state) : true, op = busy(opKey(k.conn, k.uuid));
  const head = (acts) => `<div class="head">
      <div class="title">${sideButton()}<button id="m-back">${icon("arrow-left")}${t("返回")}</button>
        <div><h1>${esc(k.name)}${m ? ` <span class="badge ${esc(m.state)}">${esc(t(m.state))}</span>` : ""}${op ? ` <span class="op" role="status">${icon("loader-circle", "spin")}${esc(op)}…</span>` : ""}</h1>
        ${m && m.title ? `<div class="meta">${esc(m.title)}</div>` : ""}</div></div>
      <div class="actions" id="m-power">${acts}</div>
    </div>`;
  if (!m) {
    main.innerHTML = head(`<button data-a="reload">${icon("refresh-cw")}${t("刷新")}</button>`) + (k.error
      ? errorCard(t("读取虚拟机失败"), k.error) : skeleton() + skeleton(true));
    wireHead();
    return;
  }
  const { shown, more } = machineActions(m, {
    console: isOff ? null : {},
    mid: [
      ...(m.state === "running" || m.state === "idle" || m.state === "paused" ? [{ a: "save", label: t("保存状态"), disabled: !!op, title: op ? t("{what} 还没完成", { what: op }) : t("把内存保存到主机上并关机, 下次启动时从这里继续") }] : []),
      { a: "xml", label: t("编辑 XML") },
      { a: "clone", label: t("克隆…"), disabled: !(isOff && !m.saved) || !!op, title: op ? t("{what} 还没完成", { what: op }) : !isOff ? t("关机后才能克隆") : m.saved ? t("它有保存的状态: 先启动再关机, 或丢弃保存的状态") : "" },
      { a: "rename", label: t("改名…"), disabled: !isOff, title: isOff ? "" : t("关机后才能改名") },
      "-",
      { a: "reload", label: t("从主机重新读取") },
    ],
  });
  main.innerHTML = head(shown.map((x) => actionButton(x)).join("") + moreMenu(more, { label: t("更多") })) + `
    ${k.error ? errorCard(t("读取虚拟机失败"), k.error) : ""}
    ${k.fresh && isOff ? `<div class="card note" id="m-fresh"><p>${esc(t("{name} 已创建, 还没有启动。在下面各页调整硬件 (CPU、磁盘、网卡、设备…), 完成后再启动它开始安装。", { name: k.name }))}</p>
      <div class="buttons"><button class="primary" id="m-fresh-start">${icon("play")}${t("完成, 启动")}</button></div></div>` : ""}
    ${m.saved ? `<div class="card note"><p>${t("它的内存状态保存在主机上, 下次启动时从保存处继续。")}</p>
      <div class="buttons"><button id="m-dropsave">${t("丢弃保存的状态")}</button></div></div>` : ""}
    ${tabBar("m-tabs", tabs.map((x) => [x.id, x.label()]), k.tab)}
    <div id="m-body"></div>`;
  wireHead();
  if ($("#m-dropsave")) $("#m-dropsave").onclick = dropSave;
  if ($("#m-fresh-start")) $("#m-fresh-start").onclick = async () => { await act("start"); if (!off(cur?.info?.state)) k.fresh = false; };
  wireTabs($("#m-tabs"), (id) => { k.tab = id; app.render(); });
  if (body) { $("#m-body").replaceWith(body); return; }
  const tab = tabs.find((x) => x.id === k.tab) || tabs[0];
  const el = $("#m-body");
  el.innerHTML = tab.html(k);
  tab.wire?.(k, el);
}

addTab({
  id: "overview",
  label: () => t("概览"),
  html: (k) => {
    const m = k.info;
    return `<div class="cards">${overview(m)}${about(m)}</div>${disks(m)}${nics(m)}${graphics(m)}`;
  },
  wire: () => {
    $("#m-autostart").onchange = (e) => set({ autostart: e.target.checked });
    $("#m-about").onclick = editAbout;
  },
});

function wireHead() {
  $("#m-back").onclick = () => app.back();
  document.querySelectorAll("#m-power [data-a]").forEach((b) => { b.onclick = () => act(b.dataset.a); });
}

const row = (k, v) => `<dt>${esc(k)}</dt><dd>${v}</dd>`;

function overview(m) {
  const mem = m.memory_kib && m.memory_kib !== m.max_memory_kib
    ? t("{cur}, 最多 {max}", { cur: kib(m.memory_kib), max: kib(m.max_memory_kib) }) : kib(m.max_memory_kib);
  const fw = { bios: "BIOS", uefi: "UEFI", "uefi-secure": t("UEFI + 安全启动") }[m.firmware] || m.firmware || "–";
  return `<div class="card"><h3>${t("概况")}</h3><dl class="kv">
    ${row(t("状态"), `<span class="badge ${esc(m.state)}">${esc(t(m.state))}</span>${m.saved ? ` <small class="mute">${t("(已保存)")}</small>` : ""}`)}
    ${row("vCPU", esc(m.vcpus || "–"))}
    ${row(t("内存"), esc(mem))}
    ${row("CPU", `<span class="mono">${esc(m.cpu || "–")}</span>`)}
    ${row(t("固件"), esc(fw))}
    ${row(t("机器类型"), `<span class="mono">${esc([m.arch, m.machine].filter(Boolean).join(" · ") || "–")}</span>`)}
    ${row(t("虚拟化"), esc(m.virt_type || "–"))}
    ${row(t("自动启动"), `<label class="switch"><input type="checkbox" id="m-autostart"${m.autostart ? " checked" : ""}${m.persistent ? "" : " disabled"}><span>${t("随主机启动")}</span></label>`)}
    ${m.persistent ? "" : row(t("定义"), t("临时 (关机后消失)"))}
    ${row("UUID", `<span class="mono sel">${esc(m.uuid)}</span>`)}
  </dl></div>`;
}

function about(m) {
  const g = m.guest;
  return `<div class="card"><h3>${t("说明")}</h3>
    <dl class="kv">
      ${row(t("标题"), m.title ? esc(m.title) : `<span class="mute">–</span>`)}
      ${row(t("描述"), m.description ? `<span class="pre">${esc(m.description)}</span>` : `<span class="mute">–</span>`)}
      ${g ? row(t("主机名"), esc(g.hostname || "–")) + row(t("操作系统"), esc(g.os || "–")) : ""}
    </dl>
    <div class="buttons"><button id="m-about">${icon("pencil")}${t("编辑标题和描述")}</button></div></div>`;
}

function disks(m) {
  const ds = m.devices.disks;
  return `<section><h2>${t("磁盘")}</h2>${ds.length === 0 ? `<p class="mute">${t("没有磁盘: 在「磁盘与光驱」里添加。")}</p>` : `<div class="tablewrap"><table><thead><tr>
      <th>${t("设备")}</th><th>${t("类型")}</th><th class="opt2">${t("总线")}</th><th>${t("来源")}</th><th>${t("大小")}</th>
    </tr></thead><tbody>${ds.map((d) => `<tr>
      <td class="mono">${esc(d.target)}</td>
      <td>${esc(t(d.device))}${d.readonly ? ` <small>${t("只读")}</small>` : ""}</td>
      <td class="opt2">${esc(d.bus)}</td>
      <td class="mono wrap">${d.source ? esc(d.source) : `<span class="mute">${t("空")}</span>`}${d.format ? ` <small>${esc(d.format)}</small>` : ""}</td>
      <td class="num">${d.capacity ? gib(d.capacity) : "–"}</td>
    </tr>`).join("")}</tbody></table></div>`}</section>`;
}

// nicAddrs are the guest's addresses on the interface mac.
export const nicAddrs = (m, mac) => m.addresses.filter((a) => a.mac && a.mac.toLowerCase() === mac.toLowerCase()).map((a) => `${a.addr}/${a.prefix}`);

// addrHint says where the addresses came from, or why there are none.
export function addrHint(m) {
  const src = { agent: t("来自客户机代理"), lease: t("来自 DHCP 租约"), arp: t("来自 ARP 表") }[m.address_source] || "";
  return off(m.state) ? t("开机后显示客户机的地址。") : m.addresses.length ? esc(src) : esc(t("还没有读到地址") + (m.address_note ? ": " + m.address_note : ""));
}

function nics(m) {
  const ns = m.devices.nics;
  const addrs = (mac) => nicAddrs(m, mac);
  return `<section><h2>${t("网卡")}</h2>${ns.length === 0 ? `<p class="mute">${t("没有网卡: 在「网卡」里添加。")}</p>` : `<div class="tablewrap"><table><thead><tr>
      <th>MAC</th><th>${t("连接到")}</th><th class="opt2">${t("型号")}</th><th class="opt">${t("链路")}</th><th>${t("地址")}</th>
    </tr></thead><tbody>${ns.map((n) => `<tr>
      <td class="mono">${esc(n.mac)}</td>
      <td class="wrap">${esc(nicSource(n))}</td>
      <td class="opt2">${esc(n.model || "–")}</td>
      <td class="opt">${n.link === "down" ? `<span class="warn">${t("断开")}</span>` : t("连通")}</td>
      <td class="mono wrap">${esc(addrs(n.mac).join(", ")) || `<span class="mute">–</span>`}</td>
    </tr>`).join("")}</tbody></table></div>
    <p class="hint">${addrHint(m)}</p>`}</section>`;
}

export function nicSource(n) {
  switch (n.kind) {
    case "network": return t("虚拟网络 {name}", { name: n.source });
    case "bridge": return t("网桥 {name}", { name: n.source });
    case "user": return t("用户态网络 (NAT)");
    case "direct": return t("直连 {name}", { name: n.source });
  }
  return n.source ? `${n.kind}: ${n.source}` : n.kind;
}

function graphics(m) {
  const gs = m.devices.graphics;
  return `<section><h2>${t("图形")}</h2>${gs.length === 0 ? `<p class="mute">${t("没有图形控制台: 在「其他设备」里添加 VNC。")}</p>` : `<div class="tablewrap"><table><thead><tr>
      <th>${t("类型")}</th><th>${t("监听地址")}</th><th>${t("端口")}</th>
    </tr></thead><tbody>${gs.map((g) => `<tr>
      <td>${esc(g.type.toUpperCase())}</td>
      <td class="mono">${esc(g.listen || t("默认"))}</td>
      <td class="num">${g.port ? esc(g.port) : g.autoport ? `<span class="mute">${t("自动")}</span>` : "–"}</td>
    </tr>`).join("")}</tbody></table></div>`}</section>`;
}

// ---- actions ----------------------------------------------------------------

async function act(a) {
  const k = cur, m = k.info, d = { uuid: k.uuid, name: k.name, state: m.state };
  switch (a) {
    case "console": return app.openConsole(k.conn, d);
    case "xml": return app.openXML(k.conn, d);
    case "delete":
      if (await app.deleteMachine(k.conn, d) && cur === k) app.back();
      return;
    case "reload": return loadMachine();
    case "rename": return rename();
    case "clone": return clone();
    case "save":
      try {
        await runOp(opKey(k.conn, k.uuid), t("保存 {name} 的状态", { name: k.name }), () => call("Save", k.conn, k.uuid));
        toast(t("已保存 {name} 的状态并关机; 下次启动时从这里继续", { name: k.name }));
      } catch (e) { fail(e); }
      return loadMachine();
    case "reset":
      if (!(await confirmDanger(t("重置"), t("重置 {name}? 相当于按下复位键, 客户机没有写入磁盘的数据会丢失。", { name: k.name })))) return;
  }
  await app.power(k.conn, d, a);
  loadMachine();
}

export async function set(settings) {
  const k = cur;
  try { showEdited(await call("Set", k.conn, k.uuid, settings)); } catch (e) { fail(e); }
  loadMachine();
}

function editAbout() {
  const k = cur, m = k.info;
  formDialog({
    title: t("{name} 的标题和描述", { name: k.name }),
    body: `<label><span>${t("标题")}</span><input name="title" value="${esc(m.title || "")}" autocomplete="off"></label>
      <label><span>${t("描述")}</span><textarea name="description" rows="5">${esc(m.description || "")}</textarea></label>`,
    ok: t("保存"),
    submit: async (f) => {
      const s = {};
      if (f.title.value !== (m.title || "")) s.title = f.title.value;
      if (f.description.value !== (m.description || "")) s.description = f.description.value;
      if (Object.keys(s).length === 0) return;
      showEdited(await call("Set", k.conn, k.uuid, s));
      loadMachine();
    },
  });
}

function rename() {
  const k = cur;
  formDialog({
    title: t("给 {name} 改名", { name: k.name }),
    body: `<label><span>${t("新名称")}</span><input name="name" value="${esc(k.name)}" spellcheck="false" autocomplete="off" required></label>`,
    ok: t("改名"),
    submit: async (f) => {
      const name = f.name.value.trim();
      if (!name || name === k.name) return;
      await call("Rename", k.conn, k.uuid, name);
      toast(t("已改名为 {name}", { name }));
      loadMachine();
      app.reloadList(k.conn);
    },
  });
}

function clone() {
  const k = cur;
  formDialog({
    title: t("克隆 {name}", { name: k.name }),
    body: `<label><span>${t("新虚拟机的名称")}</span><input name="name" value="${esc(k.name + "-clone")}" spellcheck="false" autocomplete="off" required></label>
      <p class="hint">${t("磁盘会复制到原磁盘所在的存储池; 光驱里的 ISO 和共享磁盘不复制。新虚拟机有新的 UUID 和 MAC 地址。")}</p>`,
    ok: t("克隆"),
    busy: t("复制磁盘中…"),
    submit: async (f) => {
      const name = f.name.value.trim();
      const c = await runOp(opKey(k.conn, k.uuid), t("把 {name} 克隆为 {to}", { name: k.name, to: name }), () => call("Clone", k.conn, k.uuid, name));
      toast([t("已克隆为 {name}", { name: c.domain.name }), ...c.disks.map((x) => `${x.from} → ${x.to}`), ...(c.notes || [])].join("\n"));
      app.reloadList(k.conn);
    },
  });
}

async function dropSave() {
  const k = cur;
  if (!(await confirmDanger(t("丢弃保存的状态"), t("丢弃 {name} 保存的内存状态? 下次启动会重新开机, 保存时没写入磁盘的数据会丢失。", { name: k.name }), t("丢弃")))) return;
  try { await call("DropSave", k.conn, k.uuid); } catch (e) { fail(e); }
  loadMachine();
}
