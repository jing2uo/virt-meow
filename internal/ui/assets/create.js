// The new-machine form, as virt-manager's: what virt-mm create takes, the
// disk bus and NIC model, CPU and memory checked against the host, booting
// from an ISO, a copy of a cloud or disk image, or an existing disk; then
// the XML step, or "customize before install": the new machine's details
// page, not started, with Start when done.
import { call, upload, web } from "./rpc.js";
import { t } from "./i18n.js";
import { $, esc, toast, fail, kib, gib, icon } from "./util.js";
import { xmlProblem, editorKeys, discard, guardClose } from "./xml.js";
import { runOp } from "./ops.js";

// app: reloadList(conn) after a machine is made, customize(conn, domain)
// to open its details page.
let app = null;
export function initCreate(hooks) { app = hooks; }

let createConn = null, createOpts = null, osList = null;
// shown counts the forms opened: a creation that ends after its form was
// closed (it goes on) leaves alone the one shown by then.
let shown = 0;
const fc = $("#form-create");

export async function openCreate(id) {
  shown++;
  createConn = id;
  createOpts = null;
  osList = null;
  fc.innerHTML = `<h2>${t("新建虚拟机")}</h2><p class="mute">${t("读取主机信息…")}</p>`;
  $("#dlg-create").showModal();
  try {
    // osinfo-db is a help, not a need: the form works without it.
    [createOpts, osList] = await Promise.all([call("CreateOptions", id), call("OSVariants", id).catch((e) => ({ error: e.message || String(e), variants: [] }))]);
  } catch (e) {
    fc.innerHTML = `<h2>${t("新建虚拟机")}</h2><p class="error">${esc(e.message || e)}</p>
      <div class="buttons"><button type="button" data-close>${t("取消")}</button></div>`;
    fc.querySelector("[data-close]").onclick = () => $("#dlg-create").close();
    return;
  }
  renderCreate();
  detect(readCreate());
}

const GiB = 1 << 30;
const osLabel = (o) => `${o.name} (${o.short_id})`;
const osByLabel = (text) => osList.variants.find((o) => osLabel(o) === text || o.short_id === text);

// proposed is the form's values with what osinfo-db proposes for o.
function proposed(v, o, note) {
  const p = o.propose;
  return { ...v, osv: o.id, osvText: osLabel(o), osvNote: note, os: p.os, vcpus: p.vcpus, mem: p.memory_mib / 1024,
    size: p.disk_gib, fw: p.firmware, tpm: p.tpm, bus: p.disk_bus, nic: p.nic_model };
}

// detect asks what the chosen ISO or image is, and fills in the form
// from it while that is still what the form shows.
async function detect(v) {
  const ref = v.src === "iso" ? v.iso : v.src === "image" ? v.image : "";
  if (!ref || !osList.variants.length) return;
  const [pool, vol] = ref.split(/\/(.*)/s);
  let d;
  try { d = await call("DetectOS", createConn, pool, vol); } catch { return; }
  const now = fc.elements.name ? readCreate() : null;
  if (!now || (now.src === "iso" ? now.iso : now.image) !== ref) return;
  if (!d.variant) {
    renderCreate({ ...now, osvNote: d.volume_id ? t("没认出卷标 {id}, 请选择系统", { id: d.volume_id }) : t("没从名字认出系统, 请选择") });
    return;
  }
  const how = d.volume_id ? t("按卷标 {id}", { id: d.volume_id }) : t("按文件名");
  renderCreate(proposed(now, d.variant, t("识别为 {name} ({how}), 已按它填写 CPU、内存、磁盘、固件和设备。", { name: d.variant.name, how })));
}
const opt = (value, label, sel, dis) => `<option value="${esc(value)}"${value === sel ? " selected" : ""}${dis ? " disabled" : ""}>${esc(label)}</option>`;
const volRef = (x) => x.pool + "/" + x.name;
const imageOf = (ref) => createOpts.disks.find((x) => volRef(x) === ref);

// renderCreate draws the form from createOpts, keeping what was typed.
function renderCreate(keep = {}) {
  const o = createOpts, h = o.host, nd = o.defaults || {};
  const v = {
    name: "", os: "linux", vcpus: 2, mem: 4, src: "iso", iso: o.isos.length ? volRef(o.isos[0]) : "",
    disk: "new", pool: "", size: 40, image: o.disks.length ? volRef(o.disks[0]) : "", existing: o.disks.length ? volRef(o.disks[0]) : "",
    bus: nd.disk_bus || "", net: "", nic: nd.nic_model || "", fw: "", tpm: null, start: true, xml: false, custom: false,
    fmt: nd.disk_format || "", cpu: nd.cpu_mode || "", sound: !!nd.sound, listen: nd.listen || "",
    ci: null, ciUser: "", ciPass: "", ciKeys: "", ciHost: "", ciData: "", osv: "", osvText: "", osvNote: "", ...keep,
  };
  // A cloud image does nothing useful without cloud-init: on for it.
  if (v.ci === null) v.ci = v.src === "image";
  const activePools = o.pools.filter((p) => p.active);
  // The default pool and network where the host has them, else virt-meow's.
  if (!v.pool && activePools.some((p) => p.name === nd.pool)) v.pool = nd.pool;
  if (!v.pool) v.pool = (activePools.slice().sort((a, b) => b.available - a.available)[0] || {}).name || "";
  const nets = [
    ...o.bridges.map((b) => ({ value: "bridge:" + b, label: t("网桥 {name}", { name: b }) })),
    ...o.networks.filter((n) => n.active).map((n) => ({ value: "network:" + n.name, label: t("虚拟网络 {name}", { name: n.name }) + (n.forward ? ` (${n.forward})` : "") })),
    { value: "none", label: t("不联网") },
  ];
  if (!v.net && nets.some((n) => n.value === nd.network)) v.net = nd.network;
  if (!v.net) v.net = nets[0].value;
  const win = v.os === "windows";
  if (!v.fw) v.fw = h.uefi ? (win && h.secure_boot ? "uefi-secure" : "uefi") : "bios";
  if (v.tpm === null) v.tpm = win && h.tpm;
  const maxCPU = Math.max(1, Math.min(h.max_vcpus || 64, (h.cpus || 1) * 4));
  const maxMem = Math.max(1, Math.floor((h.memory_kib || 0) / (1 << 20)));
  const img = imageOf(v.image);
  const imgGiB = img ? Math.ceil(img.capacity / GiB) : 1;
  const poolSel = `<label><span>${t("存储池")}</span><select name="pool">${activePools.map((p) => opt(p.name, `${p.name} (${gib(p.available)} free)`, v.pool)).join("")}</select></label>`;
  const fmtSel = `<label><span>${t("格式")}</span><select name="fmt">${opt("", "qcow2", v.fmt)}${opt("raw", "raw", v.fmt)}</select></label>`;
  const radio = (name, value, label, sel, dis) => `<label><input type="radio" name="${name}" value="${value}"${value === sel ? " checked" : ""}${dis ? " disabled" : ""}>${label}</label>`;

  let source = "";
  if (v.src === "iso") {
    source = `<div class="inline"><select name="iso">${opt("", t("不使用"), v.iso)}${o.isos.map((x) => opt(volRef(x), `${volRef(x)} (${gib(x.capacity)})`, v.iso)).join("")}</select>
        <button type="button" id="upload-iso">${icon("upload")}${t("上传 ISO…")}</button></div>
      <div class="radio">${radio("disk", "new", t("新建系统盘"), v.disk)}${radio("disk", "none", t("不要磁盘 (从介质运行)"), v.disk)}</div>
      ${v.disk === "new" ? `<div class="grid">${poolSel}
        <label><span>${t("大小 (GiB)")}</span><input name="size" type="number" min="1" value="${v.size}"></label>${fmtSel}</div>` : ""}`;
  } else if (v.src === "image") {
    source = `<label><span>${t("镜像")}</span><select name="image">${o.disks.map((x) => opt(volRef(x), `${volRef(x)} (${x.format || x.type}, ${gib(x.capacity)})`, v.image)).join("")}</select></label>
      <div class="grid">${poolSel}
        <label><span>${t("大小 (GiB)")}</span><input name="size" type="number" min="${imgGiB}" value="${Math.max(v.size, imgGiB)}"></label>${fmtSel}</div>
      <p class="hint">${esc(t("系统盘是镜像的一份 {fmt} 副本, 扩到这个大小; 镜像本身不变。", { fmt: v.fmt || "qcow2" }))}</p>`;
  } else {
    source = `<label><span>${t("磁盘")}</span><select name="existing">${o.disks.map((x) => opt(volRef(x), `${volRef(x)} (${x.format || x.type}, ${gib(x.capacity)})`, v.existing)).join("")}</select></label>
      <p class="hint">${t("直接用这个卷作系统盘, 不复制。")}</p>`;
  }

  fc.innerHTML = `<h2>${t("新建虚拟机")}</h2>
    <p class="hint">${esc(t("主机: {cpus} CPU, {mem} 内存 ({free} 空闲), libvirt {lv}, {virt}", { cpus: h.cpus, mem: kib(h.memory_kib), free: kib(h.free_kib), lv: h.libvirt_version, virt: h.virt_type }))}</p>
    <div class="grid">
      <label><span>${t("名称")}</span><input name="name" value="${esc(v.name)}" placeholder="web01" spellcheck="false" required></label>
      <label><span>${t("系统")}</span><select name="os">
        ${opt("linux", "Linux", v.os)}${opt("windows", "Windows", v.os)}${opt("other", t("其他"), v.os)}</select></label>
    </div>
    <label><span>${t("操作系统 (osinfo-db)")}</span><input name="osv" list="osv-list" value="${esc(v.osvText)}" placeholder="${esc(t("输入名称搜索, 例如 debian12、Windows 11"))}" spellcheck="false"${osList.variants.length ? "" : " disabled"}></label>
    <datalist id="osv-list">${osList.variants.map((o) => `<option value="${esc(osLabel(o))}">${o.eol ? esc(t("停止支持于 {d}", { d: o.eol })) : ""}</option>`).join("")}</datalist>
    <p class="hint" id="osv-note">${esc(v.osvNote || (osList.error ? t("没有 osinfo-db: {err}", { err: osList.error })
      : osList.source === "host" ? t("用主机的 osinfo-db {v}。", { v: osList.version }) : t("主机没有 osinfo-db, 用 virt-meow 自带的 {v}。", { v: osList.version })))}</p>
    ${win ? `<p class="hint">${t("Windows 默认用 SATA 磁盘和 e1000e 网卡, 不需要额外驱动。")}</p>` : ""}
    <fieldset><legend>${t("CPU 与内存")}</legend><div class="grid">
      <label><span>${t("vCPU 数")}</span><input name="vcpus" type="number" min="1" max="${maxCPU}" value="${v.vcpus}"></label>
      <label><span>${t("内存 (GiB)")}</span><input name="mem" type="number" min="0.25" step="0.25" max="${maxMem}" value="${v.mem}"></label>
    </div><p class="hint" id="create-fit"></p></fieldset>
    <fieldset><legend>${t("从哪里启动")}</legend>
      <div class="radio">${radio("src", "iso", t("安装介质 (ISO)"), v.src)}${radio("src", "image", t("云镜像或磁盘镜像 (复制)"), v.src, !o.disks.length)}${radio("src", "existing", t("使用现有磁盘"), v.src, !o.disks.length)}</div>
      ${source}
      <label><span>${t("磁盘总线")}</span><select name="bus">${opt("", t("按系统 ({bus})", { bus: win ? "SATA" : "virtio" }), v.bus)}${opt("virtio", "virtio", v.bus)}${opt("sata", "SATA", v.bus)}${opt("scsi", "SCSI (virtio-scsi)", v.bus)}</select></label>
    </fieldset>
    ${v.src === "iso" ? "" : `<fieldset><legend>cloud-init</legend>
      <label class="check"><input type="checkbox" name="ci"${v.ci ? " checked" : ""}><span>${t("首次启动时用 cloud-init 设置 (NoCloud 种子 ISO, 作为光驱挂上)")}</span></label>
      ${v.ci ? `<div class="grid">
        <label><span>${t("用户 (可选)")}</span><input name="ciUser" value="${esc(v.ciUser)}" placeholder="${esc(t("镜像的默认用户"))}" spellcheck="false"></label>
        <label><span>${t("主机名")}</span><input name="ciHost" value="${esc(v.ciHost)}" placeholder="${esc(t("同虚拟机名称"))}" spellcheck="false"></label>
        <label><span>${t("密码 (需要用户)")}</span><input name="ciPass" type="password" value="${esc(v.ciPass)}" autocomplete="new-password"></label>
      </div>
      <label><span>${t("SSH 公钥")}</span><textarea name="ciKeys" rows="2" spellcheck="false" placeholder="ssh-ed25519 AAAA… user@host">${esc(v.ciKeys)}</textarea></label>
      <details${v.ciData ? " open" : ""}><summary>${t("自己的 user-data (可选)")}</summary>
        <textarea name="ciData" class="mono" rows="5" spellcheck="false" placeholder="#cloud-config">${esc(v.ciData)}</textarea>
        <p class="hint">${t("填了就原样使用, 上面的用户、密码和公钥不再生成, 写进它里面。")}</p></details>
      <p class="hint">${t("密码只以 SHA-512 哈希写进种子。没有用户时, 公钥给镜像的默认用户 (debian、ubuntu…)。")}</p>` : ""}
    </fieldset>`}
    <fieldset><legend>${t("网络")}</legend><div class="grid">
      <label><span>${t("连接到")}</span><select name="net">${nets.map((n) => opt(n.value, n.label, v.net)).join("")}</select></label>
      <label><span>${t("网卡型号")}</span><select name="nic"${v.net === "none" ? " disabled" : ""}>${opt("", t("按系统 ({model})", { model: win ? "e1000e" : "virtio" }), v.nic)}${["virtio", "e1000e", "e1000", "rtl8139"].map((m) => opt(m, m, v.nic)).join("")}</select></label>
    </div></fieldset>
    <fieldset><legend>${t("固件")}</legend><div class="grid">
      <select name="fw">${opt("bios", "BIOS", v.fw)}${opt("uefi", "UEFI", v.fw, !h.uefi)}${opt("uefi-secure", t("UEFI + 安全启动"), v.fw, !h.secure_boot)}</select>
      <label class="check"><input type="checkbox" name="tpm"${v.tpm ? " checked" : ""}${h.tpm ? "" : " disabled"}><span>${t("TPM 2.0 (Windows 11 需要)")}</span></label>
    </div></fieldset>
    <fieldset><legend>${t("CPU 模式、控制台和声音")}</legend><div class="grid">
      <label><span>${t("CPU 模式")}</span><select name="cpu"${h.virt_type === "kvm" ? "" : " disabled"}>${opt("", "host-passthrough", v.cpu)}${opt("host-model", "host-model", v.cpu)}</select></label>
      <label><span>${t("VNC 监听地址")}</span><input name="listen" value="${esc(v.listen)}" placeholder="127.0.0.1" spellcheck="false"></label>
    </div>
    ${h.virt_type === "kvm" ? "" : `<p class="hint">${t("主机没有 KVM: 虚拟机用 QEMU 模拟的 CPU。")}</p>`}
    ${v.listen && !/^(127\.|::1$)/.test(v.listen) ? `<p class="warn">${esc(t("在 {addr} 上监听: 网络上的任何人都能不加密、不用密码地打开它的控制台。", { addr: v.listen }))}</p>` : ""}
    <label class="check"><input type="checkbox" name="sound"${v.sound ? " checked" : ""}><span>${t("声卡")}</span></label>
    <p class="hint">${t("默认值在设置里改。")}</p></fieldset>
    <label class="check"><input type="checkbox" name="start"${v.start && !v.custom ? " checked" : ""}${v.custom ? " disabled" : ""}><span>${t("创建后立即启动")}</span></label>
    <label class="check"><input type="checkbox" name="xml"${v.xml ? " checked" : ""}><span>${t("创建前编辑 XML")}</span></label>
    <label class="check"><input type="checkbox" name="custom"${v.custom ? " checked" : ""}><span>${t("安装前自定义: 创建但不启动, 打开它的详情页, 改完再启动")}</span></label>
    <p class="error" id="create-error"></p>
    <div class="buttons">
      <button type="button" data-close>${t("取消")}</button>
      <button type="submit" class="primary" id="create-ok">${t(v.xml ? "下一步" : "创建")}</button>
    </div>`;
  const f = fc.elements;
  f.xml.onchange = () => { $("#create-ok").textContent = t(f.xml.checked ? "下一步" : "创建"); };
  f.custom.onchange = () => { f.start.disabled = f.custom.checked; if (f.custom.checked) f.start.checked = false; };
  fc.querySelector("[data-close]").onclick = () => $("#dlg-create").close();
  // Choices that change what the form shows, or its defaults, redraw it.
  const redraw = (patch) => renderCreate({ ...readCreate(), ...patch });
  f.os.onchange = () => redraw({ fw: "", tpm: null, size: f.os.value === "windows" ? 64 : readCreate().size });
  f.net.onchange = () => redraw({});
  f.listen.onchange = () => redraw({});
  fc.querySelectorAll("[name=disk]").forEach((r) => { r.onchange = () => redraw({}); });
  fc.querySelectorAll("[name=src]").forEach((r) => { r.onchange = () => { redraw({}); detect(readCreate()); }; });
  if (f.image) f.image.onchange = () => { redraw({}); detect(readCreate()); };
  if (f.iso) f.iso.onchange = () => detect(readCreate());
  f.osv.onchange = () => {
    const o = osByLabel(f.osv.value.trim());
    if (o) renderCreate(proposed(readCreate(), o, t("已按 {name} 填写 CPU、内存、磁盘、固件和设备。", { name: o.name })));
    else if (!f.osv.value.trim()) redraw({ osv: "", osvText: "", osvNote: "" });
    else $("#osv-note").textContent = t("osinfo-db 里没有这个系统");
  };
  if (f.ci) f.ci.onchange = () => redraw({});
  f.vcpus.oninput = f.mem.oninput = showFit;
  showFit();
  if ($("#upload-iso")) $("#upload-iso").onclick = async () => {
    const pool = readCreate().pool || (activePools[0] || {}).name;
    if (!web) {
      try { await call("PickUpload", createConn, pool); } catch (e) { fail(e); }
      return;
    }
    // In a browser the file is on the user's computer, not virt-meow's: the
    // page sends it.
    const input = document.createElement("input");
    input.type = "file";
    input.accept = ".iso,.img,.qcow2,.raw";
    input.onchange = () => {
      const file = input.files[0];
      if (file) upload(createConn, pool, file).catch(fail);
    };
    input.click();
  };
  if (!v.name) f.name.focus();
}

// showFit says how the CPUs and memory asked for fit the host: beyond its
// free memory or its CPUs is allowed (hv's warnings), beyond its memory
// or what it can give a machine is not (hv's Check).
function showFit() {
  const h = createOpts.host, f = fc.elements, el = $("#create-fit");
  const mem = +f.mem.value * (1 << 20), cpus = +f.vcpus.value;
  const lines = [], bad = [];
  if (h.memory_kib && mem > h.memory_kib) bad.push(t("内存超过主机的 {mem}", { mem: kib(h.memory_kib) }));
  else if (h.free_kib && mem > h.free_kib) lines.push(t("内存超过主机现在空闲的 {free}: 可能启动不了, 或用到交换", { free: kib(h.free_kib) }));
  const maxCPU = Math.min(h.max_vcpus || 64, (h.cpus || 1) * 4);
  if (cpus > maxCPU) bad.push(t("vCPU 最多 {n}", { n: maxCPU }));
  else if (h.cpus && cpus > h.cpus) lines.push(t("vCPU 多于主机的 {n} 个 CPU: 能运行, 但它们分着用", { n: h.cpus }));
  el.textContent = [...bad, ...lines].join("; ") || t("主机放得下。");
  el.className = bad.length ? "error" : lines.length ? "hint warn" : "hint";
}

function readCreate() {
  const f = fc.elements;
  const readFmt = createOpts.defaults?.disk_format || "";
  return {
    name: f.name.value.trim(), os: f.os.value, vcpus: +f.vcpus.value, mem: +f.mem.value,
    src: fc.querySelector("[name=src]:checked")?.value || "iso",
    iso: f.iso?.value || "", disk: fc.querySelector("[name=disk]:checked")?.value || "new",
    pool: f.pool?.value || "", size: +(f.size?.value || 40), image: f.image?.value || "", existing: f.existing?.value || "",
    bus: f.bus.value, net: f.net.value, nic: f.nic.value,
    fmt: f.fmt ? f.fmt.value : readFmt, cpu: f.cpu.value, sound: f.sound.checked, listen: f.listen.value.trim(),
    fw: f.fw.value, tpm: f.tpm.checked, start: f.start.checked, xml: f.xml.checked, custom: f.custom.checked,
    ci: f.ci ? f.ci.checked : null, ciUser: f.ciUser?.value.trim() || "", ciPass: f.ciPass?.value || "",
    ciKeys: f.ciKeys?.value || "", ciHost: f.ciHost?.value.trim() || "", ciData: f.ciData?.value || "",
    osvText: f.osv.value.trim(), osv: osByLabel(f.osv.value.trim())?.id || "", osvNote: $("#osv-note")?.textContent || "",
  };
}

const split = (ref) => ref ? [ref.split("/")[0], ref.split(/\/(.*)/s)[1] || ""] : ["", ""];

// specOf is the hv.Spec the form's values describe.
function specOf(v) {
  const [isoPool, isoVol] = v.src === "iso" ? split(v.iso) : ["", ""];
  const [imgPool, imgVol] = v.src === "image" ? split(v.image) : ["", ""];
  const [impPool, impVol] = v.src === "existing" ? split(v.existing) : ["", ""];
  const newDisk = v.src === "image" || (v.src === "iso" && v.disk === "new");
  const [netKind, netSource] = v.net === "none" ? ["none", ""] : v.net.split(/:(.*)/s);
  return {
    name: v.name, os: v.os, vcpus: v.vcpus, memory_mib: Math.round(v.mem * 1024),
    disk_pool: newDisk ? v.pool : "", disk_gib: newDisk ? v.size : 0,
    import_pool: impPool, import_volume: impVol,
    iso_pool: isoPool, iso_volume: isoVol,
    net_kind: netKind, net_source: netSource || "",
    firmware: v.fw, tpm: v.tpm, start: v.start && !v.custom,
    image_pool: imgPool, image_volume: imgVol, disk_bus: v.bus, nic_model: netKind === "none" ? "" : v.nic, os_variant: v.osv,
    listen: v.listen, cpu_mode: v.cpu, disk_format: newDisk ? v.fmt : "", sound: v.sound,
    cloud_init: v.src !== "iso" && v.ci
      ? { user: v.ciUser, password: v.ciPass, ssh_keys: v.ciKeys.trim(), hostname: v.ciHost, user_data: v.ciData }
      : null,
  };
}

// createXML is the second step of the form, when the user edits the
// definition before creating: the spec it came from, the form's values to go
// back to, and the XML as generated.
let createXML = null;

function renderCreateXML(x) {
  const sp = createXML.spec;
  $("#dlg-create").classList.add("xml");
  fc.innerHTML = `<h2>${esc(t("新建虚拟机 {name}: XML", { name: sp.name }))}</h2>
    <p class="hint">${t("按表单生成的定义, 改完再创建; libvirt 校验通过才会创建。")}
      ${sp.disk_pool ? esc(t("新磁盘 {pool}/{vol} ({size} GiB) 在创建时生成, 从 XML 里删掉它就不会生成。", { pool: sp.disk_pool, vol: sp.name + (sp.disk_format === "raw" ? ".img" : ".qcow2"), size: sp.disk_gib })) : ""}</p>
    <textarea class="xml" name="xml" spellcheck="false" wrap="off" autocomplete="off"></textarea>
    <p class="hint">${t("Tab 缩进, Ctrl+S 创建。")}</p>
    <p class="error" id="create-error"></p>
    <div class="buttons">
      <button type="button" class="left" id="create-back">${t("返回表单")}</button>
      <button type="button" id="create-cancel">${t("取消")}</button>
      <button type="submit" class="primary" id="create-ok">${t("创建")}</button>
    </div>`;
  const ta = fc.elements.xml;
  ta.value = x;
  editorKeys(ta, () => fc.requestSubmit());
  $("#create-back").onclick = async () => {
    if (ta.value !== createXML.base && !(await discard())) return;
    const keep = createXML.keep;
    leaveCreateXML();
    renderCreate(keep);
  };
  $("#create-cancel").onclick = async () => {
    if (ta.value !== createXML.base && !(await discard())) return;
    $("#dlg-create").close();
  };
  ta.focus();
  ta.setSelectionRange(0, 0);
}

function leaveCreateXML() {
  createXML = null;
  $("#dlg-create").classList.remove("xml");
}
$("#dlg-create").addEventListener("close", leaveCreateXML);
guardClose($("#dlg-create"), () => createXML && fc.elements.xml && fc.elements.xml.value !== createXML.base);

fc.addEventListener("submit", async (e) => {
  e.preventDefault();
  const step = createXML;
  const v = step ? step.keep : readCreate();
  const spec = step ? step.spec : specOf(v);
  const x = step ? fc.elements.xml.value : "";
  const btn = $("#create-ok");
  $("#create-error").textContent = "";
  if (step) {
    const p = xmlProblem(x);
    if (p) { $("#create-error").textContent = p; return; }
  } else if (v.xml) {
    btn.disabled = true;
    try {
      const gen = await call("PreviewMachine", createConn, spec);
      createXML = { spec, keep: v, base: gen };
      renderCreateXML(gen);
    } catch (err) {
      $("#create-error").textContent = String(err.message || err).trim();
      btn.disabled = false;
    }
    return;
  }
  const label = btn.textContent;
  btn.disabled = true;
  btn.innerHTML = `${icon("loader-circle", "spin")}${esc(spec.image_volume ? t("复制镜像中…") : t("创建中…"))}`;
  const conn = createConn, mine = shown, here = () => shown === mine && $("#dlg-create").open;
  try {
    const c = await runOp(`c:${conn}/${spec.name}`, t("新建 {name}", { name: spec.name }), () => call("CreateMachine", conn, spec, x));
    if (here()) $("#dlg-create").close();
    const notes = (c.warnings || []).map((w) => "⚠ " + w);
    if (c.start_error) toast(t("{name} 已创建, 但没能启动: {err}", { name: c.domain.name, err: c.start_error }), true);
    else toast([t("已创建 {name}", { name: c.domain.name }), ...notes].join("\n"), notes.length > 0);
    app.reloadList(conn);
    if (v.custom) app.customize(conn, c.domain);
  } catch (err) {
    if (!here()) return toast(t("新建 {name} 失败: {err}", { name: spec.name, err: String(err.message || err).trim() }), true);
    $("#create-error").textContent = String(err.message || err).trim();
    btn.disabled = false;
    btn.textContent = label;
  }
});

// uploaded: a finished ISO shows up in the open creation form, selected.
export function uploaded(u) {
  if (u.state !== "done" || !createOpts || u.connection !== createConn || !$("#dlg-create").open || createXML) return;
  const keep = readCreate();
  call("CreateOptions", createConn).then((o) => {
    createOpts = o;
    renderCreate({ ...keep, src: "iso", iso: u.pool + "/" + u.name });
    detect(readCreate());
  }).catch(fail);
}
