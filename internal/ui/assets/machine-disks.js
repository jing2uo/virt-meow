// The details page's disks and CD/DVD drives: what virt-mm disk and virt-mm cd
// do. The volumes to pick from are CreateOptions' (every active pool's).
import { call, on, upload, web } from "./rpc.js";
import { t } from "./i18n.js";
import { $, esc, fail, gib, formDialog, showEdited, confirmDanger, icon, emptyState } from "./util.js";
import { addTab, loadMachine, off, shownMachine, opKey } from "./machine.js";
import { runOp, busy } from "./ops.js";

const opt = (value, label, sel) => `<option value="${esc(value)}"${value === sel ? " selected" : ""}>${esc(label)}</option>`;

// edit runs one change and reads the machine again; a dialog's submit lets
// its error stay under the fields instead.
async function edit(fn) {
  try { showEdited(await fn()); } catch (e) { fail(e); }
  loadMachine();
}

addTab({
  id: "disks",
  label: () => t("磁盘与光驱"),
  html: (k) => {
    const m = k.info, ds = m.devices.disks, running = !off(m.state);
    return `<section>
      <div class="toolbar"><button id="d-add">${icon("plus")}${t("添加磁盘…")}</button><button id="d-cd">${icon("disc")}${t("添加光驱…")}</button></div>
      ${ds.length === 0 ? emptyState({ small: true, icon: "hard-drive", title: t("没有磁盘"), text: t("添加一块新磁盘或已有的卷; 从网络或光盘启动的机器可以没有。") }) : `<div class="tablewrap"><table><thead><tr>
        <th>${t("设备")}</th><th>${t("类型")}</th><th class="opt2">${t("总线")}</th><th>${t("来源")}</th><th>${t("大小")}</th><th></th>
      </tr></thead><tbody>${ds.map((d, i) => `<tr>
        <td class="mono">${esc(d.target)}</td>
        <td>${esc(t(d.device))}${d.readonly && d.device !== "cdrom" ? ` <small>${t("只读")}</small>` : ""}</td>
        <td class="opt2">${esc(d.bus)}</td>
        <td class="mono wrap">${d.source ? esc(d.source) : `<span class="mute">${t("空")}</span>`}${d.format ? ` <small>${esc(d.format)}</small>` : ""}</td>
        <td class="num">${d.capacity ? gib(d.capacity) : "–"}</td>
        <td class="ops"><div>${d.device === "cdrom"
          ? `<button data-i="${i}" data-a="insert">${icon("disc")}${t(d.source ? "换 ISO…" : "插入 ISO…")}</button>${d.source ? `<button data-i="${i}" data-a="eject">${t("弹出")}</button>` : ""}`
          : d.device === "disk" ? `<button data-i="${i}" data-a="grow"${busy(opKey(k.conn, k.uuid)) ? " disabled" : ""}>${icon("hard-drive")}${t("扩容…")}</button>` : ""}<button class="del" data-i="${i}" data-a="remove">${icon("trash-2")}${t("移除")}</button></div></td>
      </tr>`).join("")}</tbody></table></div>`}
      <p class="hint">${running ? t("运行中: virtio、SCSI 和 USB 磁盘立即接上, SATA 磁盘和新光驱在下次启动时才有; 删除卷要先关机。")
        : t("磁盘只能扩大不能缩小; 客户机里的分区和文件系统要在客户机里扩。")}</p>
    </section>`;
  },
  wire: (k, body) => {
    const ds = k.info.devices.disks;
    $("#d-add", body).onclick = () => addDisk(k);
    $("#d-cd", body).onclick = () => insertISO(k, null);
    body.querySelectorAll("td.ops button").forEach((b) => {
      const d = ds[+b.dataset.i];
      b.onclick = () => ({ insert: () => insertISO(k, d), eject: () => edit(() => call("EjectCD", k.conn, k.uuid, d.target)), grow: () => grow(k, d), remove: () => remove(k, d) })[b.dataset.a]();
    });
  },
});

// options reads the pools and volumes once per dialog: they change outside
// the page.
async function options(k) {
  try { return await call("CreateOptions", k.conn); } catch (e) { fail(e); return null; }
}

async function addDisk(k) {
  const o = await options(k);
  if (!o) return;
  const pools = o.pools.filter((p) => p.active);
  const vols = o.disks;
  formDialog({
    title: t("给 {name} 添加磁盘", { name: k.name }),
    body: `<div class="radio"><label><input type="radio" name="kind" value="new" checked>${t("新建卷")}</label>
        <label><input type="radio" name="kind" value="existing"${vols.length ? "" : " disabled"}>${t("已有的卷")}</label></div>
      <div id="dk-new" class="row">
        <label><span>${t("存储池")}</span><select name="pool">${pools.map((p) => opt(p.name, p.name, pools[0]?.name)).join("")}</select></label>
        <label class="narrow"><span>${t("大小 (GiB)")}</span><input name="size" type="number" min="1" value="20"></label>
      </div>
      <label id="dk-existing" hidden><span>${t("卷")}</span><select name="volume">${vols.map((v) => opt(v.pool + "/" + v.name, `${v.pool}/${v.name} (${gib(v.capacity)})`, "")).join("")}</select></label>
      <label><span>${t("总线")}</span><select name="bus">${opt("", t("与第一块磁盘相同"), "")}${["virtio", "sata", "scsi", "usb"].map((b) => opt(b, b, "")).join("")}</select></label>
      <p class="hint">${esc(t("新卷是 qcow2, 命名为 <虚拟机>-<设备>.qcow2。一个卷同时给两台运行的虚拟机写会损坏它。"))}</p>`,
    ok: t("添加"),
    busy: t("添加中…"),
    submit: async (f) => {
      const nd = { bus: f.bus.value };
      if (f.kind.value === "new") {
        if (!f.pool.value) throw new Error(t("没有活动的存储池"));
        Object.assign(nd, { pool: f.pool.value, size_gib: +f.size.value });
      } else {
        const v = vols[f.volume.selectedIndex];
        Object.assign(nd, { vol_pool: v.pool, volume: v.name });
      }
      showEdited(await call("AddDisk", k.conn, k.uuid, nd));
      loadMachine();
    },
  });
  const f = $("#form-dialog");
  const show = () => {
    $("#dk-new", f).hidden = f.elements.kind.value !== "new";
    $("#dk-existing", f).hidden = f.elements.kind.value === "new";
  };
  f.querySelectorAll("[name=kind]").forEach((r) => { r.onchange = show; });
  if (!pools.length && vols.length) {
    f.elements.kind.value = "existing";
    show();
  }
}

// isoDialog is the open insert dialog, to show an ISO uploaded from it.
let isoDialog = null;

async function insertISO(k, drive) {
  const o = await options(k);
  if (!o) return;
  const pools = o.pools.filter((p) => p.active);
  const isoOpts = (isos, sel) => isos.length ? isos.map((v) => opt(v.pool + "/" + v.name, `${v.pool}/${v.name} (${gib(v.capacity)})`, sel)).join("")
    : `<option value="">${esc(t("还没有 ISO, 先上传一个"))}</option>`;
  formDialog({
    title: drive ? t("{target} 里放入 ISO", { target: drive.target }) : t("给 {name} 添加光驱", { name: k.name }),
    body: `<label><span>ISO</span><div class="inline"><select name="iso">${isoOpts(o.isos, drive?.source || "")}</select>
        <button type="button" id="iso-up">${icon("upload")}${t("上传 ISO…")}</button></div></label>
      <label><span>${t("上传到存储池")}</span><select name="pool">${pools.map((p) => opt(p.name, p.name, "")).join("")}</select></label>
      <p class="hint">${drive ? t("运行中的虚拟机立即换盘。") : t("新光驱接在 SATA (q35) 或 IDE 上; 运行中的虚拟机下次启动时才有。")}</p>`,
    ok: drive ? t("放入") : t("添加"),
    submit: async (f) => {
      if (!f.iso.value) throw new Error(t("先选一个 ISO"));
      const [pool, ...rest] = f.iso.value.split("/");
      showEdited(await call("InsertCD", k.conn, k.uuid, drive ? drive.target : "", pool, rest.join("/")));
      loadMachine();
    },
  });
  const f = $("#form-dialog");
  isoDialog = { k, f, isoOpts };
  $("#dlg-form").addEventListener("close", () => { isoDialog = null; }, { once: true });
  $("#iso-up", f).onclick = async () => {
    const pool = f.elements.pool.value;
    if (!pool) return fail(t("没有活动的存储池"));
    if (!web) {
      try { await call("PickUpload", k.conn, pool); } catch (e) { fail(e); }
      return;
    }
    const input = document.createElement("input");
    input.type = "file";
    input.accept = ".iso,.img";
    input.onchange = () => { if (input.files[0]) upload(k.conn, pool, input.files[0]).catch(fail); };
    input.click();
  };
}

// An ISO uploaded while the dialog is open shows in it, selected.
on("upload", async (u) => {
  const d = isoDialog;
  if (!d || u.state !== "done" || u.connection !== d.k.conn || shownMachine() !== d.k) return;
  let o;
  try { o = await call("CreateOptions", d.k.conn); } catch (e) { return fail(e); }
  if (isoDialog === d) d.f.elements.iso.innerHTML = d.isoOpts(o.isos, u.pool + "/" + u.name);
});

function grow(k, d) {
  const cur = Math.ceil(d.capacity / (1 << 30));
  formDialog({
    title: t("扩容 {target}", { target: d.target }),
    body: `<label><span>${t("新大小 (GiB)")}</span><input name="size" type="number" min="${cur}" value="${cur + 10}"></label>
      <p class="hint">${esc(t("现在 {size}。只能扩大; 之后在客户机里扩分区和文件系统。", { size: d.capacity ? gib(d.capacity) : "–" }))}</p>`,
    ok: t("扩容"),
    submit: async (f) => {
      showEdited(await runOp(opKey(k.conn, k.uuid), t("把 {name} 的 {target} 扩到 {size} GiB", { name: k.name, target: d.target, size: f.size.value }),
        () => call("ResizeDisk", k.conn, k.uuid, d.target, +f.size.value * (1 << 30))));
      loadMachine();
    },
  });
}

function remove(k, d) {
  const running = !off(k.info.state), cd = d.device === "cdrom";
  if (cd || !d.source) {
    confirmDanger(t("移除"), t("从 {name} 移除 {target}?", { name: k.name, target: d.target }), t("移除"))
      .then((ok) => ok && edit(() => call("RemoveDisk", k.conn, k.uuid, d.target, false)));
    return;
  }
  formDialog({
    title: t("移除 {target}", { target: d.target }),
    body: `<p>${esc(t("从 {name} 移除 {target} ({source})?", { name: k.name, target: d.target, source: d.source }))}</p>
      <label class="check"><input type="checkbox" name="del"${running ? " disabled" : ""}>${t("同时删除它的卷 (不可恢复)")}</label>
      ${running ? `<p class="hint">${t("运行中: 只能移除磁盘, 关机后才能删除卷。")}</p>` : ""}`,
    ok: t("移除"),
    danger: true,
    submit: async (f) => {
      showEdited(await call("RemoveDisk", k.conn, k.uuid, d.target, f.del.checked));
      loadMachine();
    },
  });
}
