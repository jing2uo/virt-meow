// What happened, newest first: the actions taken from the page and their
// outcome, machines' events on every host, uploads. The log is virt-meow's
// (api.Activity), kept for its session and, under serve, shared by every
// page; this page adds its own actions to it (rpc.js reports each call).
import { call, on, onCall } from "./rpc.js";
import { t } from "./i18n.js";
import { $, esc, icon } from "./util.js";

// Calls that change nothing aren't actions; nor are the consoles opening
// and uploads (virt-meow logs an upload's outcome itself).
const quiet = new Set(["Env", "Connections", "SSHRoute", "Domains", "Overview", "Info", "Definition", "Devices", "CPUModels",
  "Stats", "HostStats", "Host", "HostDevices", "HostDeviceXML", "Networks", "Bridges", "NetworkDefinition", "DHCPHosts",
  "Leases", "Snapshots", "Pools", "Volumes", "Thumbnail", "SSHKeys", "CreateOptions", "OSVariants", "DetectOS",
  "PreviewMachine", "Answer", "OpenConsole", "CloseConsole", "GrabKeyboard", "OpenSerial", "PickUpload", "CancelUpload",
  "PickDownload", "CancelDownload", "Activity", "RecordAction", "OpenConsoleWindow", "RememberView"]);

// What an action is called, by Service method.
const labels = {
  Power: "电源操作", PowerMany: "批量电源操作", CreateMachine: "新建虚拟机", DeleteMachine: "删除虚拟机",
  Redefine: "保存 XML", Set: "修改设置", Rename: "改名", Clone: "克隆", Save: "保存状态", DropSave: "丢弃保存的状态",
  SetBoot: "启动顺序", AddDisk: "添加磁盘", RemoveDisk: "移除磁盘", ResizeDisk: "扩容磁盘", InsertCD: "插入光盘",
  EjectCD: "弹出光盘", AddNIC: "添加网卡", RemoveNIC: "移除网卡", SetLink: "网卡链路", SetVideo: "显卡", SetSound: "声卡",
  AddGraphics: "添加控制台", SetGraphicsListen: "控制台监听地址", RemoveGraphics: "移除控制台", AddTPM: "添加 TPM",
  RemoveTPM: "移除 TPM", AddRNG: "添加随机数生成器", RemoveRNG: "移除随机数生成器", AddClipboard: "共享剪贴板", RemoveClipboard: "停止共享剪贴板", AttachDevice: "按 XML 添加设备",
  DetachDevice: "按 XML 移除设备", AttachHostDevice: "添加主机设备", DetachHostDevice: "移除主机设备",
  AddFilesystem: "添加共享目录", RemoveFilesystem: "移除共享目录", CreateSnapshot: "创建快照", RevertSnapshot: "恢复快照",
  DeleteSnapshot: "删除快照", AddPool: "添加存储池", SetPool: "存储池操作", RemovePool: "移除存储池", CreateVolume: "新建卷",
  ResizeVolume: "扩容卷", DeleteVolume: "删除卷", AddNetwork: "添加网络", SetNetwork: "网络操作", RemoveNetwork: "移除网络",
  RedefineNetwork: "保存网络 XML", AddDHCPHost: "添加固定地址", RemoveDHCPHost: "移除固定地址",
  SetUserPassword: "设置用户密码", SetSSHKeys: "修改 SSH 公钥", AddConnection: "添加连接", RemoveConnection: "移除连接",
  Connect: "连接主机", TrustHostKey: "信任主机密钥", ForgetSecrets: "忘记记住的密码",
};
const events = {
  started: "启动了", stopped: "停止了", paused: "暂停了", resumed: "继续运行", shutdown: "客户机正在关机",
  crashed: "崩溃了", defined: "定义已保存", undefined: "已删除", "pm-suspended": "客户机睡眠了", other: "其他事件",
};

// app: names for the log, connName(id) and machineName(conn, uuid).
let app = null;
const log = []; // newest first
let unread = 0, open = false, filter = "";

const uuidRE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

// entryOf is what is logged of a call: its connection and machine, and
// the short arguments that say what it did. Long ones (XML, keys) are
// left out, and passwords never go in.
function entryOf(method, args, err) {
  const e = { what: method, error: err ? String(err.message || err).trim() : "" };
  if (typeof args[0] === "string" && app.connName(args[0])) e.connection = args[0];
  let rest = args.slice(1);
  if (typeof args[1] === "string" && uuidRE.test(args[1])) {
    e.machine = app.machineName(args[0], args[1]) || args[1];
    rest = args.slice(2);
  } else if (method === "CreateMachine" && args[1]) {
    e.machine = args[1].name;
    rest = [];
  }
  if (method === "SetUserPassword") rest = rest.slice(0, 1); // the user, not the password
  if (method === "Power") rest = [t(args[2])];
  if (method === "PowerMany") {
    rest = [t("{n} 台: {action}", { n: args[0].length, action: t(args[1]) })];
    e.machine = args[0].map((r) => app.machineName(r.connection, r.uuid) || r.uuid).join(", ");
  }
  if (method === "Set" && args[2]) rest = [Object.keys(args[2]).join(", ")];
  e.detail = rest.filter((a) => (typeof a === "string" && a.length <= 80) || typeof a === "number").join(" ");
  return e;
}

export function initActivity(hooks) {
  app = hooks;
  onCall((method, args, err) => {
    if (quiet.has(method)) return;
    call("RecordAction", entryOf(method, args, err)).catch(() => {});
  });
  on("activity", (a) => {
    if (log.some((x) => x.id === a.id)) return;
    log.unshift(a);
    if (log.length > 500) log.length = 500;
    if (!open) unread++;
    draw();
  });
  on("virt-mm:reconnected", load);
  document.addEventListener("virt-mm:activity", () => { open = true; unread = 0; draw(); });
  $("#activity-btn").onclick = () => { open = !open; unread = 0; draw(); };
  load();
}

// closeActivity closes the panel; true when it was open.
export function closeActivity() {
  if (!open) return false;
  open = false;
  draw();
  return true;
}

async function load() {
  try {
    const all = await call("Activity");
    log.splice(0, log.length, ...all);
  } catch { /* the panel says nothing new */ }
  draw();
}

const time = (s) => new Date(s).toLocaleTimeString([], { hour12: false });

function line(a) {
  const host = a.connection ? app.connName(a.connection) || a.connection : "";
  let what;
  if (a.kind === "event") what = `${esc(a.machine)} ${esc(t(events[a.what] || a.what))}${a.detail ? ` <small>${esc(a.detail)}</small>` : ""}`;
  else if (a.kind === "upload") what = `${esc(t("上传 {name}", { name: a.what }))} <small>${esc(a.detail)}</small>`;
  else what = `${esc(t(labels[a.what] || a.what))}${a.machine ? " " + esc(a.machine) : ""}${a.detail ? ` <small>${esc(a.detail)}</small>` : ""}`;
  const failed = a.error ? " err" : "";
  return `<li class="act ${esc(a.kind)}${failed}">
    <div class="top"><span class="mono">${esc(time(a.time))}</span><span class="kind">${esc(t({ action: "操作", event: "事件", upload: "上传" }[a.kind] || a.kind))}</span>${host ? `<span class="host">${esc(host)}</span>` : ""}</div>
    <div>${what}${a.kind !== "event" ? (a.error ? ` <span class="bad">${esc(t("失败"))}</span>` : ` <span class="good">${esc(t("成功"))}</span>`) : ""}</div>
    ${a.error ? `<div class="mono errtext">${esc(a.error)}</div>` : ""}
  </li>`;
}

function draw() {
  const btn = $("#activity-btn");
  btn.querySelector(".count").textContent = unread ? String(unread) : "";
  btn.classList.toggle("on", open);
  let panel = $("#activity");
  if (!open) { panel?.remove(); return; }
  if (!panel) {
    panel = document.createElement("section");
    panel.id = "activity";
    document.body.append(panel);
  }
  const shown = log.filter((a) => !filter || a.kind === filter || (filter === "action" && a.kind === "upload"));
  const chip = (k, label) => `<button type="button" data-f="${k}" class="${filter === k ? "on" : ""}">${esc(label)}</button>`;
  panel.innerHTML = `<div class="head"><h2>${t("动态")}</h2><button type="button" class="icon" id="activity-close" aria-label="${esc(t("关闭"))}" title="${esc(t("关闭"))}">${icon("x", "i-lg")}</button></div>
    <div class="chips">${chip("", t("全部"))}${chip("action", t("操作"))}${chip("event", t("事件"))}</div>
    <p class="hint">${t("这次运行以来, 最新的在上面。")}</p>
    ${shown.length ? `<ul class="acts">${shown.map(line).join("")}</ul>` : `<p class="mute">${t("还没有动态: 在这里做的操作和虚拟机的事件会出现在这里。")}</p>`}`;
  $("#activity-close").onclick = () => { open = false; draw(); };
  panel.querySelectorAll("[data-f]").forEach((b) => { b.onclick = () => { filter = b.dataset.f; draw(); }; });
}
