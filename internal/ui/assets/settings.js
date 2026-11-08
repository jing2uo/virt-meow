// The page's settings (api.Settings, kept in the config dir): language,
// desktop notifications (the window's), the list's pictures and the
// sidebar, in a small dialog; and the desktop notifications themselves.
import { call, web } from "./rpc.js";
import { t } from "./i18n.js";
import { $, esc, fail, formDialog } from "./util.js";
import { initSide } from "./layout.js";
import { setThumbsNever } from "./thumbs.js";

// prefs are the settings as last read or saved.
export let prefs = { lang: "", notifications: true, thumbnails: "", sidebar: "", new_machine: {} };
let redraw = () => {};

// loadSettings reads the settings and applies them; changed() redraws the
// page after a change that shows in it.
export async function loadSettings(changed) {
  redraw = changed;
  try { prefs = await call("Settings"); } catch { /* the defaults */ }
  apply();
}

function apply() {
  setThumbsNever(prefs.thumbnails === "off");
  initSide(prefs.sidebar === "hidden", (hidden) => save({ sidebar: hidden ? "hidden" : "" }).catch(fail));
}

async function save(patch) {
  prefs = await call("SaveSettings", { ...prefs, ...patch });
  return prefs;
}

export function openSettings() {
  const opt = (v, label, cur) => `<option value="${esc(v)}"${v === cur ? " selected" : ""}>${esc(label)}</option>`;
  formDialog({
    title: t("设置"),
    body: `<label><span>${t("语言")}</span><select name="lang">${opt("", t("跟随系统"), prefs.lang)}${opt("zh", "中文", prefs.lang)}${opt("en", "English", prefs.lang)}</select></label>
      ${web ? "" : `<label class="check"><input type="checkbox" name="notifications"${prefs.notifications ? " checked" : ""}><span>${t("桌面通知")}</span></label>
      <p class="hint">${t("长时间的操作完成、或虚拟机自己停下 (关机、崩溃) 时, 如果 virt-meow 的窗口不在前台, 就通知桌面。")}</p>`}
      <label><span>${t("列表里的画面")}</span><select name="thumbnails">${opt("", t("每台主机自己的开关"), prefs.thumbnails)}${opt("off", t("全部关闭"), prefs.thumbnails)}</select></label>
      <label><span>${t("侧栏 (宽窗口)")}</span><select name="sidebar">${opt("", t("展开"), prefs.sidebar)}${opt("hidden", t("收起"), prefs.sidebar)}</select></label>
      ${newMachineFields(prefs.new_machine || {}, opt)}
      <p class="hint">${t("设置保存在 virt-meow 的配置目录里; virt-mm serve 的页面和窗口共用。")}</p>`,
    ok: t("保存"),
    submit: async (f) => {
      const before = prefs.lang;
      const kind = f.nmNetKind.value, netName = f.nmNetName.value.trim();
      if ((kind === "network" || kind === "bridge") && !netName) throw new Error(t("写出网络或网桥的名称"));
      await save({
        lang: f.lang.value, thumbnails: f.thumbnails.value, sidebar: f.sidebar.value,
        notifications: web ? prefs.notifications : f.notifications.checked,
        new_machine: {
          listen: f.nmListen.value.trim(), cpu_mode: f.nmCPU.value, disk_format: f.nmFormat.value, disk_bus: f.nmBus.value,
          nic_model: f.nmNIC.value, sound: f.nmSound.checked, pool: f.nmPool.value.trim(),
          network: kind === "network" || kind === "bridge" ? `${kind}:${netName}` : kind,
        },
      });
      if (prefs.lang !== before) { location.reload(); return; } // every string again
      apply();
      redraw();
    },
  });
  const f = $("#form-dialog").elements;
  f.nmNetKind.onchange = () => { f.nmNetName.disabled = !["network", "bridge"].includes(f.nmNetKind.value); };
}

// newMachineFields are the defaults new machines get, in the form and
// virt-mm create (virt-mm defaults): "" leaves each to virt-meow.
function newMachineFields(n, opt) {
  const [kind, name] = n.network ? (n.network === "none" ? ["none", ""] : n.network.split(/:(.*)/s)) : ["", ""];
  return `<fieldset class="nm"><legend>${t("新建虚拟机的默认值")}</legend>
    <div class="grid">
      <label><span>${t("存储池")}</span><input name="nmPool" value="${esc(n.pool || "")}" placeholder="${esc(t("空间最多的"))}" spellcheck="false"></label>
      <label><span>${t("网络")}</span><span class="inline"><select name="nmNetKind">${opt("", t("自动"), kind)}${opt("network", t("虚拟网络"), kind)}${opt("bridge", t("网桥"), kind)}${opt("none", t("不联网"), kind)}</select>
        <input name="nmNetName" value="${esc(name || "")}" placeholder="default" spellcheck="false" aria-label="${esc(t("网络或网桥的名称"))}"${kind === "network" || kind === "bridge" ? "" : " disabled"}></span></label>
      <label><span>${t("磁盘格式")}</span><select name="nmFormat">${opt("", "qcow2", n.disk_format || "")}${opt("raw", "raw", n.disk_format || "")}</select></label>
      <label><span>${t("磁盘总线")}</span><select name="nmBus">${opt("", t("按系统"), n.disk_bus || "")}${["virtio", "sata", "scsi"].map((x) => opt(x, x === "sata" ? "SATA" : x === "scsi" ? "SCSI (virtio-scsi)" : x, n.disk_bus || "")).join("")}</select></label>
      <label><span>${t("网卡型号")}</span><select name="nmNIC">${opt("", t("按系统"), n.nic_model || "")}${["virtio", "e1000e", "e1000", "rtl8139"].map((x) => opt(x, x, n.nic_model || "")).join("")}</select></label>
      <label><span>${t("CPU 模式 (KVM)")}</span><select name="nmCPU">${opt("", "host-passthrough", n.cpu_mode || "")}${opt("host-model", "host-model", n.cpu_mode || "")}</select></label>
      <label><span>${t("VNC 监听地址")}</span><input name="nmListen" value="${esc(n.listen || "")}" placeholder="127.0.0.1" spellcheck="false"></label>
    </div>
    <label class="check"><input type="checkbox" name="nmSound"${n.sound ? " checked" : ""}><span>${t("声卡")}</span></label>
    <p class="hint">${t("新建表单和 virt-mm create 都用它们; 主机上没有的存储池或网络不会被选中。系统的 osinfo-db 条目对磁盘总线和网卡型号的建议优先。host-model 便于迁移, host-passthrough 性能最好。")}</p>
  </fieldset>`;
}

// notify tells the desktop something the user may be waiting for, when
// the settings allow; virt-meow sends it only while none of its windows has
// focus. Only the window's main page: a console's own window would say
// it again, and a browser shows its toasts.
export function notify(title, body = "") {
  if (web || !prefs.notifications || new URLSearchParams(location.search).has("solo")) return;
  call("Notify", title, body).catch(() => {});
}

$("#settings-btn")?.addEventListener("click", openSettings);
