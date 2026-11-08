// A connection's storage tab: its pools and the volumes of the one picked,
// what virt-mm pool and virt-mm vol do. Uploads go as in the creation form;
// a download is a save dialog in the window and a plain download in a
// browser (serve's GET /api/download).
import { call, on, upload, web } from "./rpc.js";
import { t } from "./i18n.js";
import { $, esc, fail, errText, gib, toast, formDialog, confirmDanger, icon, errorCard, emptyState, skeleton } from "./util.js";
import { moreMenu } from "./menu.js";
import { runOp } from "./ops.js";

// cache: conn -> { pools, sel, vols, at, err, volErr }. The page redraws
// the tab on every machine event: the lists are read again when older
// than FRESH, drawn from here meanwhile.
const FRESH = 3000;
const cache = new Map();
let shown = null;

export function renderStorage(body, conn) {
  shown = conn;
  const c = cache.get(conn) || { sel: "", at: 0 };
  cache.set(conn, c);
  body.insertAdjacentHTML("beforeend", `<div id="storage">${draw(conn)}</div>`);
  wire(conn);
  if (Date.now() - c.at > FRESH) load(conn);
}

async function load(conn) {
  const c = cache.get(conn);
  c.at = Date.now();
  try {
    c.pools = await call("Pools", conn);
    c.err = "";
  } catch (e) { c.err = errText(e); }
  if (c.pools && !c.pools.some((p) => p.name === c.sel)) c.sel = (c.pools.find((p) => p.active) || c.pools[0] || {}).name || "";
  await loadVols(conn);
}

async function loadVols(conn) {
  const c = cache.get(conn), p = c.pools?.find((x) => x.name === c.sel);
  c.vols = null;
  c.volErr = "";
  if (p && p.active) {
    try { c.vols = await call("Volumes", conn, p.name); } catch (e) { c.volErr = errText(e); }
  }
  redraw(conn);
}

function redraw(conn) {
  if (shown !== conn || !$("#storage")) return;
  // Redrawn under the keyboard: focus stays on the same row's same thing.
  const f = document.activeElement, row = f?.closest?.("#storage tr[data-i]"), table = row?.closest("table")?.className;
  const what = f === row ? "" : f?.dataset?.a || "";
  $("#storage").innerHTML = draw(conn);
  wire(conn);
  if (row) {
    const r = $(`#storage table.${table} tr[data-i="${row.dataset.i}"]`);
    (what && r?.querySelector(`[data-a="${what}"]`) || r)?.focus({ preventScroll: true });
  }
}

const used = (p) => (p.capacity ? Math.round((p.allocation / p.capacity) * 100) : 0);

function draw(conn) {
  const c = cache.get(conn);
  const pools = c.err ? errorCard(t("读不到存储池"), c.err)
    : !c.pools ? skeleton(true)
    : !c.pools.length ? emptyState({ small: true, icon: "hard-drive", title: t("还没有存储池"), text: t("存储池是放磁盘和 ISO 的地方: 添加一个目录、LVM 卷组或 NFS 导出。") })
    : `<div class="tablewrap"><table class="pools"><thead><tr>
        <th>${t("名称")}</th><th class="opt2">${t("类型")}</th><th class="opt">${t("位置")}</th><th>${t("状态")}</th><th class="opt2">${t("自动启动")}</th><th>${t("容量")}</th><th></th>
      </tr></thead><tbody>${c.pools.map((p, i) => `<tr class="open${p.name === c.sel ? " sel" : ""}" data-i="${i}" tabindex="0" aria-selected="${p.name === c.sel}">
        <td><b>${esc(p.name)}</b></td>
        <td class="opt2">${esc(poolType(p.type))}</td>
        <td class="mono wrap opt">${esc(p.path)}${p.source ? `<br><small>${esc(p.source)}</small>` : ""}</td>
        <td><span class="badge ${p.active ? "running" : "shutoff"}">${p.active ? t("已启动") : t("未启动")}</span></td>
        <td class="opt2">${p.autostart ? "✓" : ""}</td>
        <td class="num">${p.active && p.capacity ? `<div class="bar" title="${esc(t("已用 {pct}%", { pct: used(p) }))}"><div style="width:${used(p)}%"></div></div>
          <small>${esc(t("{free} 可用, 共 {size}", { free: gib(p.available), size: gib(p.capacity) }))}</small>` : "–"}</td>
        <td class="ops"><div>${p.active ? `<button data-i="${i}" data-a="stop">${icon("power")}${t("停止")}</button>` : `<button data-i="${i}" data-a="start">${icon("play")}${t("启动")}</button>`}${moreMenu([
          { a: "autostart", label: p.autostart ? t("关闭自动启动") : t("自动启动"), data: { i } }, "-",
          { a: "remove", label: t("移除"), cls: "del", data: { i } }])}</div></td>
      </tr>`).join("")}</tbody></table></div>`;
  return `<section><h2>${t("存储池")}</h2>
      <div class="toolbar"><button id="p-add">${icon("plus")}${t("添加存储池…")}</button><button id="p-reload">${icon("refresh-cw")}${t("刷新")}</button></div>
      ${pools}</section>
    ${volumes(c)}`;
}

function poolType(type) {
  return { dir: t("目录"), logical: t("LVM 卷组"), netfs: "NFS" }[type] || type;
}

function volumes(c) {
  const p = c.pools?.find((x) => x.name === c.sel);
  if (!p) return "";
  const head = `<h2>${esc(t("{pool} 里的卷", { pool: p.name }))}</h2>`;
  if (!p.active) return `<section>${head}${emptyState({ small: true, icon: "power", title: t("存储池没有启动"), text: t("启动存储池后才能看到和管理它的卷。") })}</section>`;
  const body = c.volErr ? errorCard(t("读不到卷"), c.volErr)
    : !c.vols ? skeleton(true)
    : !c.vols.length ? emptyState({ small: true, icon: "inbox", title: t("这个存储池是空的"), text: t("新建一个卷, 或上传 ISO 和磁盘镜像。") })
    : `<div class="tablewrap"><table class="vols"><thead><tr>
        <th>${t("名称")}</th><th class="opt2">${t("格式")}</th><th>${t("大小")}</th><th class="opt2">${t("占用")}</th><th class="opt">${t("路径")}</th><th></th>
      </tr></thead><tbody>${c.vols.map((v, i) => `<tr>
        <td class="mono wrap">${esc(v.name)}</td>
        <td class="opt2">${esc(v.format || v.type)}</td>
        <td class="num">${gib(v.capacity)}</td>
        <td class="num opt2">${gib(v.allocation)}</td>
        <td class="mono wrap opt"><small>${esc(v.path)}</small></td>
        <td class="ops"><div>${moreMenu([...(v.type === "dir" ? [] : [{ a: "grow", label: t("扩容…"), data: { v: i } },
          { a: "download", label: t("下载"), data: { v: i } }, "-"]), { a: "delete", label: t("删除"), cls: "del", data: { v: i } }])}</div></td>
      </tr>`).join("")}</tbody></table></div>`;
  return `<section>${head}
    <div class="toolbar"><button id="v-create">${icon("plus")}${t("新建卷…")}</button><button id="v-upload">${icon("upload")}${t("上传…")}</button></div>
    ${body}
    <p class="hint">${t("正被虚拟机用作磁盘的卷不能删除; 运行中的虚拟机的磁盘在它的「磁盘与光驱」里扩容, 客户机会知道。")}</p></section>`;
}

function wire(conn) {
  const el = $("#storage"), c = cache.get(conn);
  if (!el) return;
  $("#p-add").onclick = () => addPool(conn);
  $("#p-reload").onclick = () => load(conn);
  el.querySelectorAll("table.pools tr.open").forEach((tr) => {
    tr.onclick = (e) => {
      if (e.target.closest("button")) return;
      const p = c.pools[+tr.dataset.i];
      if (p.name === c.sel) return;
      c.sel = p.name;
      loadVols(conn);
      redraw(conn);
    };
  });
  el.querySelectorAll("table.pools td.ops [data-a]").forEach((b) => {
    const p = c.pools[+b.dataset.i];
    b.onclick = () => poolAction(conn, p, b.dataset.a);
  });
  const p = c.pools?.find((x) => x.name === c.sel);
  if ($("#v-create")) $("#v-create").onclick = () => createVol(conn, p);
  if ($("#v-upload")) $("#v-upload").onclick = () => uploadVol(conn, p);
  el.querySelectorAll("table.vols td.ops [data-a]").forEach((b) => {
    const v = c.vols[+b.dataset.v];
    b.onclick = () => ({ grow: growVol, download: downloadVol, delete: deleteVol })[b.dataset.a](conn, p, v);
  });
}

// ---- pools --------------------------------------------------------------

async function poolAction(conn, p, a) {
  if (a === "remove") {
    if (!(await confirmDanger(t("移除存储池"), t("移除存储池 {name}? libvirt 不再管理它; 里面的文件 (或卷组) 留在主机上, 再添加同样的存储池就能找回。", { name: p.name }), t("移除")))) return;
    try { await call("RemovePool", conn, p.name); toast(t("已移除存储池 {name}", { name: p.name })); } catch (e) { fail(e); }
    return load(conn);
  }
  if (a === "stop" && !(await confirmDanger(t("停止存储池"), t("停止存储池 {name}? 用到它里面的卷的虚拟机在它重新启动前不能开机。", { name: p.name }), t("停止")))) return;
  const action = { start: "start", stop: "stop", autostart: p.autostart ? "autostart-off" : "autostart-on" }[a];
  try { await call("SetPool", conn, p.name, action); } catch (e) { fail(e); }
  load(conn);
}

function addPool(conn) {
  formDialog({
    title: t("添加存储池"),
    body: `<label><span>${t("名称")}</span><input name="name" required spellcheck="false" autocomplete="off"></label>
      <div class="radio"><label><input type="radio" name="type" value="dir" checked>${t("目录")}</label>
        <label><input type="radio" name="type" value="logical">${t("LVM 卷组")}</label>
        <label><input type="radio" name="type" value="netfs">NFS</label></div>
      <label data-type="dir netfs"><span>${t("主机上的目录")}</span><input name="path" spellcheck="false" placeholder="/var/lib/libvirt/images/…"></label>
      <label data-type="logical"><span>${t("卷组名")}</span><input name="vg" spellcheck="false" placeholder="vg0"></label>
      <label data-type="netfs"><span>${t("NFS 导出")}</span><input name="nfs" spellcheck="false" placeholder="nas.lan:/srv/vms"></label>
      <p class="hint" data-type="dir">${esc(t("目录不存在时会创建。留空用 /var/lib/libvirt/images/<名称>。"))}</p>
      <p class="hint" data-type="logical">${t("用主机上已有的卷组; virt-meow 从不创建卷组, 那会抹掉下面的磁盘。")}</p>
      <p class="hint" data-type="netfs">${esc(t("挂载到上面的目录 (留空用 /var/lib/libvirt/images/<名称>); 主机需要能访问这个 NFS 服务器。"))}</p>
      <label class="check" data-type="netfs"><input type="checkbox" name="nostart">${t("只定义, 先不挂载")}</label>
      <label class="check"><input type="checkbox" name="autostart" checked>${t("随主机启动")}</label>`,
    ok: t("添加"),
    busy: t("添加中…"),
    submit: async (f) => {
      const type = f.type.value;
      const np = { name: f.name.value.trim(), type, autostart: f.autostart.checked };
      if (type !== "logical" && f.path.value.trim()) np.path = f.path.value.trim();
      // The form's default; hv wants the path said.
      if (type === "dir" && !np.path) np.path = "/var/lib/libvirt/images/" + np.name;
      if (type === "logical") np.source = f.vg.value.trim();
      if (type === "netfs") { np.source = f.nfs.value.trim(); np.no_start = f.nostart.checked; }
      const p = await call("AddPool", conn, np);
      toast(p.active ? t("已添加并启动存储池 {name}", { name: p.name }) : t("已添加存储池 {name} (未启动)", { name: p.name }));
      cache.get(conn).sel = p.name;
      load(conn);
    },
  });
  const f = $("#form-dialog");
  const show = () => f.querySelectorAll("[data-type]").forEach((el) => { el.hidden = !el.dataset.type.split(" ").includes(f.elements.type.value); });
  f.querySelectorAll("[name=type]").forEach((r) => { r.onchange = show; });
  show();
}

// ---- volumes ------------------------------------------------------------

function createVol(conn, p) {
  const logical = p.type === "logical";
  formDialog({
    title: t("在 {pool} 里新建卷", { pool: p.name }),
    body: `<div class="row"><label><span>${t("名称")}</span><input name="name" required spellcheck="false" autocomplete="off" value="${logical ? "" : ".qcow2"}"></label>
        <label class="narrow"><span>${t("大小 (GiB)")}</span><input name="size" type="number" min="1" value="20"></label></div>
      ${logical ? "" : `<label><span>${t("格式")}</span><select name="format"><option value="qcow2">qcow2</option><option value="raw">raw</option></select></label>`}
      <p class="hint">${logical ? t("在卷组里是一个逻辑卷 (raw)。") : t("qcow2 按需占用空间, 能拍快照; raw 一次占满, 稍快。")}</p>`,
    ok: t("创建"),
    busy: t("创建中…"),
    submit: async (f) => {
      const v = await call("CreateVolume", conn, p.name, f.name.value.trim(), +f.size.value * (1 << 30), f.format ? f.format.value : "");
      toast(t("已创建 {name}", { name: `${v.pool}/${v.name}` }));
      load(conn);
    },
  });
  const name = $("#form-dialog").elements.name;
  name.setSelectionRange(0, 0);
}

function growVol(conn, p, v) {
  const cur = Math.ceil(v.capacity / (1 << 30));
  formDialog({
    title: t("扩容 {target}", { target: v.name }),
    body: `<label><span>${t("新大小 (GiB)")}</span><input name="size" type="number" min="${cur}" value="${cur + 10}"></label>
      <p class="hint">${esc(t("现在 {size}。只能扩大; 之后在客户机里扩分区和文件系统。", { size: gib(v.capacity) }))}</p>`,
    ok: t("扩容"),
    submit: async (f) => {
      await runOp(`v:${conn}/${p.name}/${v.name}`, t("把 {name} 扩到 {size} GiB", { name: v.name, size: f.size.value }),
        () => call("ResizeVolume", conn, p.name, v.name, +f.size.value * (1 << 30)));
      toast(t("已把 {name} 扩到 {size} GiB", { name: v.name, size: f.size.value }));
      load(conn);
    },
  });
}

async function deleteVol(conn, p, v) {
  if (!(await confirmDanger(t("删除卷"), t("删除 {pool}/{name}? 它的数据会永久丢失。", { pool: p.name, name: v.name }), t("删除")))) return;
  try { await call("DeleteVolume", conn, p.name, v.name); toast(t("已删除 {name}", { name: `${p.name}/${v.name}` })); } catch (e) { fail(e); }
  load(conn);
}

function uploadVol(conn, p) {
  if (!web) {
    call("PickUpload", conn, p.name).catch(fail);
    return;
  }
  const input = document.createElement("input");
  input.type = "file";
  input.onchange = () => { if (input.files[0]) upload(conn, p.name, input.files[0]).catch(fail); };
  input.click();
}

function downloadVol(conn, p, v) {
  if (!web) {
    call("PickDownload", conn, p.name, v.name).catch(fail);
    return;
  }
  // The browser saves it, with its own progress and its own place.
  const a = document.createElement("a");
  a.href = "/api/download?" + new URLSearchParams({ connection: conn, pool: p.name, name: v.name });
  a.download = v.name;
  document.body.append(a);
  a.click();
  a.remove();
}

// A finished upload shows in the pool it went into.
on("upload", (u) => {
  if (u.state === "done" && cache.has(u.connection)) {
    cache.get(u.connection).sel = u.pool;
    if (shown === u.connection && $("#storage")) load(u.connection);
  }
});

// The window's downloads, shown as uploads are.
on("download", (d) => {
  let el = document.getElementById("dl-" + d.id);
  if (!el) {
    el = document.createElement("div");
    el.id = "dl-" + d.id;
    el.className = "upload";
    $("#uploads").append(el);
  }
  const pct = d.total ? Math.min(100, Math.floor((d.got / d.total) * 100)) : 0;
  if (d.state === "running") {
    el.innerHTML = `<div class="top"><span>${esc(t("正在下载 {name}", { name: d.name }))} — ${pct}%</span>
      <button type="button">${t("取消")}</button></div><div class="bar"><div style="width:${pct}%"></div></div>`;
    el.querySelector("button").onclick = () => call("CancelDownload", d.id);
    return;
  }
  el.remove();
  if (d.state === "done") toast(t("已下载 {name} 到 {path}", { name: d.name, path: d.path }));
  else if (d.state === "cancelled") toast(t("已取消下载 {name}", { name: d.name }));
  else toast(t("下载 {name} 失败: {err}", { name: d.name, err: d.error }), true);
});
