// A machine's console, filling the main pane: VNC through noVNC (SPICE
// through spice-html5, as it was) or the serial console through xterm.js.
// The VNC screen fills the pane, scaled with its aspect kept, or at its
// actual size; the guest's resolution can follow the pane; keys the desktop
// keeps for itself go from a menu, and text is typed in for a guest with
// no agent. It opens in a window of its own (a tab under serve), the page
// there showing only the console ("solo").
import { call, on, web, wsURL } from "./rpc.js";
import { t } from "./i18n.js";
import { $, esc, fail, toast, formDialog, infoDialog, icon, showEdited } from "./util.js";
import { sideButton } from "./layout.js";
import { moreMenu } from "./menu.js";
import { combos, sendCombo, typable, typeKeys } from "./console-keys.js";

// solo: this page shows one console and nothing else.
export const solo = new URLSearchParams(location.search).has("solo");

// cur is the console shown: its connection, machine, the kinds it has
// (vnc, spice, serial) and the one shown, and the client (noVNC,
// spice-html5 or the terminal) with the token virt-meow issued for it.
let cur = null;
let app = null; // { select(conn), render(), back(), moved() (the URL follows) }
export function initConsole(hooks) { app = hooks; }
export const shownConsole = () => cur;

// How the VNC screen is shown, remembered: fit (scaled to the pane) or
// actual size, and whether the guest's resolution follows the pane.
const VIEW = "virt-mm.console.view", RESIZE = "virt-mm.console.resize";
const view = () => localStorage.getItem(VIEW) || "fit";
const resize = () => localStorage.getItem(RESIZE) === "on";

// consoleURL is where a console opens on its own: the solo page.
export const consoleURL = (conn, name, kind) => `/?solo#/${encodeURIComponent(conn)}/console/${encodeURIComponent(name)}${kind ? "/" + kind : ""}`;

export async function openConsole(id, d, kind = "") {
  closeConsole();
  let dev;
  try { dev = await call("Devices", id, d.uuid); } catch (e) { fail(e); return; }
  // The serial console is always offered: a machine without one says so
  // in the terminal.
  const kinds = [...[...new Set((dev.graphics || []).map((g) => g.type))].filter((k) => k === "vnc" || k === "spice"), "serial"];
  cur = { conn: id, uuid: d.uuid, name: d.name, kinds, kind: kinds.includes(kind) ? kind : kinds[0], client: null, token: "", clipboard: !!dev.clipboard };
  if (solo) document.title = d.name;
  app.select(id);
  app.render();
  connect();
}

export function renderConsole(main) {
  const k = cur;
  main.dataset.view = "console";
  main.classList.add("console");
  main.innerHTML = `<div class="con-bar">
      ${solo ? "" : `${sideButton()}<button id="con-back">${icon("arrow-left")}${t("返回")}</button>`}
      <strong class="con-name">${esc(k.name)}</strong>
      <div class="seg" id="con-kind">${k.kinds.map((x) => `<button data-kind="${x}" class="${x === k.kind ? "on" : ""}">${icon(x === "serial" ? "terminal" : "monitor")}${x === "serial" ? t("串口") : x.toUpperCase()}</button>`).join("")}</div>
      <span class="con-status" id="con-status"></span>
      <span class="spacer"></span>
      <button id="con-reconnect" hidden>${icon("refresh-cw")}${t("重新连接")}</button>
      <button id="con-force" hidden title="${esc(t("断开另一个客户端, 由这里接管串口"))}">${t("强制接管")}</button>
      <button id="con-clip" hidden>${icon("copy")}<span class="lbl">${t("复制客户机的文本")}</span><span class="lbl-short">${t("复制")}</span></button>
      <button id="con-grab" hidden></button>
      <span id="con-menus"></span>
      <button id="con-full" aria-label="${esc(t("全屏"))}">${icon("maximize")}<span class="lbl">${t("全屏")}</span></button>
    </div>
    <div id="screen" class="${k.kind}"></div>`;
  if (!solo) $("#con-back").onclick = () => { closeConsole(); app.back(); };
  $("#con-kind").onclick = (e) => {
    const b = e.target.closest("button");
    if (!b || b.dataset.kind === k.kind) return;
    k.kind = b.dataset.kind;
    main.querySelectorAll("#con-kind button").forEach((x) => x.classList.toggle("on", x === b));
    $("#screen").className = k.kind;
    menus();
    showGrab();
    connect();
    app.moved();
  };
  $("#con-reconnect").onclick = () => connect();
  $("#con-force").onclick = () => connect(true);
  $("#con-full").onclick = () => fullscreen();
  $("#con-grab").onclick = () => (grab ? releaseKeys() : grabKeys());
  $("#con-clip").onclick = () => copyGuestText();
  menus();
  showGrab();
}

// The keyboard grab, as virt-manager's: while it is on, the keys the
// desktop keeps for itself (Super, Alt+Tab, Ctrl+Alt+F…) go to the guest,
// until Ctrl+Alt is pressed and let go. The window takes them from the
// desktop (GrabKeyboard); a browser through the Keyboard Lock API, which
// holds only in full screen, so grabbing there goes full screen too.
let grab = null; // { how: "window" | "lock", full: went full screen for it }

async function grabKeys() {
  const rfb = cur?.client?.rfb;
  if (!rfb || grab) return;
  let g = null;
  if (!web) {
    try { if (await call("GrabKeyboard", true)) g = { how: "window" }; } catch (e) { return fail(e); }
  }
  if (!g && navigator.keyboard?.lock) {
    try {
      const full = !document.fullscreenElement;
      if (full) await $("#main").requestFullscreen();
      await navigator.keyboard.lock();
      g = { how: "lock", full };
    } catch (e) { return fail(e); }
  }
  if (!g) return toast(t("这里截不住桌面的按键; 用「按键」菜单发送它们。"), true);
  grab = g;
  showGrab();
  rfb.focus();
}

function releaseKeys() {
  const g = grab;
  if (!g) return;
  grab = null;
  if (g.how === "window") call("GrabKeyboard", false).catch(() => {});
  else {
    navigator.keyboard?.unlock();
    if (g.full && document.fullscreenElement) document.exitFullscreen().catch(() => {});
  }
  showGrab();
  cur?.client?.rfb?.focus();
}

// showGrab draws the grab's button and state: plain when off, and saying
// how to let go when on.
function showGrab() {
  const b = $("#con-grab");
  $("#screen")?.classList.toggle("grabbed", !!grab);
  if (!b) return;
  b.hidden = cur?.kind !== "vnc";
  b.classList.toggle("grab-on", !!grab);
  const label = grab ? t("按键都发给客户机 · Ctrl+Alt 释放") : t("抓取键盘");
  // A narrow bar keeps a short label: the state must show, and the icon
  // alone is the Keys menu's.
  const short = grab ? t("Ctrl+Alt 释放") : t("抓取");
  b.innerHTML = `${icon("keyboard")}<span class="lbl">${esc(label)}</span><span class="lbl-short">${esc(short)}</span>`;
  b.title = grab ? t("Super、Alt+Tab、Ctrl+Alt+F… 都发给客户机; 按下 Ctrl+Alt 再松开就还给桌面") : t("让 Super、Alt+Tab、Ctrl+Alt+F… 这些桌面自己用的键也发给客户机");
  b.setAttribute("aria-pressed", grab ? "true" : "false");
  b.setAttribute("aria-label", label);
}

// Ctrl+Alt, pressed and let go with no other key between, releases the
// grab. Seen before noVNC takes the keys (capture), and still sent on.
let chord = false;
document.addEventListener("keydown", (e) => {
  if (!grab) return;
  chord = (e.key === "Control" || e.key === "Alt") ? (chord || (e.ctrlKey && e.altKey)) : false;
}, true);
document.addEventListener("keyup", (e) => {
  if (grab && chord && (e.key === "Control" || e.key === "Alt")) {
    chord = false;
    releaseKeys();
  }
}, true);
// The desktop ended the window's grab (focus went elsewhere, or it said
// no); a browser's ends with full screen.
on("keyboard-grab", (g) => { if (!g.on && grab?.how === "window") releaseKeys(); });
document.addEventListener("fullscreenchange", () => { if (!document.fullscreenElement && grab?.how === "lock") releaseKeys(); });

// menus draws the bar's two menus for the console shown: the keys to send
// (VNC), and how it is shown.
function menus() {
  const k = cur, el = $("#con-menus");
  if (!el) return;
  const vnc = k.kind === "vnc", graphic = k.kind !== "serial";
  const clip = k.clipboard ? { a: "paste", label: t("发送剪贴板到客户机…"), icon: "copy" }
    : { a: "clip-on", label: t("共享剪贴板…"), icon: "copy", title: t("给虚拟机加上 qemu-vdagent 通道; 客户机里需要 spice-vdagent") };
  const keys = vnc ? moreMenu([...combos.map(([id, label]) => ({ a: "key:" + id, label, icon: "" })), "-", { a: "type", label: t("输入文本…"), icon: "keyboard" }, clip],
    { label: t("按键"), icon: "keyboard", title: t("发送按键") }) : graphic ? `<button data-a="key:cad">${icon("keyboard")}Ctrl+Alt+Del</button>` : "";
  const shown = moreMenu([
    ...(vnc ? [{ a: "fit", label: t("适应窗口"), checked: view() === "fit" }, { a: "actual", label: t("实际大小"), checked: view() === "actual" },
      "-", { a: "resize", label: t("客户机分辨率跟随窗口"), checked: resize(), title: t("需要客户机支持 (例如 virtio 显卡和它的驱动)") }, "-"] : []),
    { a: "popout", label: web ? t("在新标签页中打开") : t("在单独的窗口中打开"), icon: "external-link" },
    { a: "help", label: t("按键和全屏说明"), icon: "circle-help" },
  ], { label: t("显示"), icon: "monitor", title: t("显示方式") });
  el.innerHTML = keys + shown;
  el.querySelectorAll("[data-a]").forEach((b) => { b.onclick = () => menuAction(b.dataset.a); });
}

function menuAction(a) {
  const k = cur, rfb = k.client?.rfb;
  if (a.startsWith("key:")) {
    if (a === "key:cad" && !rfb) k.client?.cad();
    else if (rfb) { sendCombo(rfb, a.slice(4)); rfb.focus(); }
    return;
  }
  switch (a) {
    case "type": return typeText();
    case "paste": return pasteText();
    case "clip-on": return clipboardOn();
    case "fit": case "actual":
      localStorage.setItem(VIEW, a);
      if (rfb) scale(rfb);
      return menus();
    case "resize":
      localStorage.setItem(RESIZE, resize() ? "off" : "on");
      if (rfb) { rfb.resizeSession = resize(); checkResize(rfb); }
      return menus();
    case "popout": return popout();
    case "help": return help();
  }
}

function scale(rfb) {
  rfb.scaleViewport = view() === "fit";
  rfb.clipViewport = false;
  $("#screen")?.classList.toggle("actual", view() === "actual");
}

// checkResize says when the guest didn't follow the pane's size: QEMU
// turns the request down unless the guest's display can change size (a
// virtio display with its driver, say), and noVNC only logs that.
function checkResize(rfb) {
  if (!resize()) return;
  setTimeout(() => {
    const c = $("#screen canvas"), s = $("#screen");
    if (cur?.client?.rfb !== rfb || !c || !s) return;
    if (Math.abs(c.width - s.clientWidth) > 16 || Math.abs(c.height - s.clientHeight) > 16) conStatus(t("已连接; 这台客户机的分辨率没有跟随窗口"));
  }, 2500);
}

// A console in its own window: the window's own (Wails), or a browser's
// tab. This view gives it up and goes back, so the two don't fight over a
// serial console.
async function popout() {
  const k = cur;
  if (web) {
    if (!window.open(consoleURL(k.conn, k.name, k.kind), "_blank")) return fail(t("浏览器拦截了新标签页"));
  } else {
    try { await call("OpenConsoleWindow", k.conn, k.name, k.kind); } catch (e) { return fail(e); }
  }
  if (!solo) { closeConsole(); app.back(); }
}

function help() {
  infoDialog(t("按键和全屏"), `<ul class="help">
    <li>${esc(t("点一下画面, 键盘和鼠标就交给客户机; 点画面外面拿回来。"))}</li>
    <li>${esc(t("「抓取键盘」让桌面自己用的键 (Super、Alt+Tab、Ctrl+Alt+F…) 也发给客户机; 按下 Ctrl+Alt 再松开就还给桌面。浏览器里它会同时进入全屏。"))}</li>
    <li>${esc(t("桌面自己会截住的组合键 (Ctrl+Alt+Del、Ctrl+Alt+F1…F12、PrintScreen) 也可以从「按键」菜单发送。"))}</li>
    <li>${esc(t("共享剪贴板时 (客户机里有 spice-vdagent), 客户机复制的文本到这台电脑的剪贴板; 这台电脑的剪贴板在点画面时给客户机 (浏览器允许读取时), 否则从「按键」里的「发送剪贴板到客户机…」。"))}</li>
    <li>${esc(t("客户机没有代理、不能粘贴时, 用「按键」里的「输入文本…」把文本一个键一个键打进去 (按美式键盘)。"))}</li>
    <li>${esc(t("离开全屏: 按 Esc, 或点这一栏的「退出全屏」。"))}</li>
  </ul>`);
}

function fullscreen() {
  if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
  else $("#main").requestFullscreen?.().catch((e) => fail(e));
}
document.addEventListener("fullscreenchange", () => {
  const b = $("#con-full");
  if (!b) return;
  const label = document.fullscreenElement ? t("退出全屏") : t("全屏");
  b.innerHTML = `${icon(document.fullscreenElement ? "minimize" : "maximize")}<span class="lbl">${esc(label)}</span>`;
  b.setAttribute("aria-label", label);
});
document.addEventListener("fullscreenchange", () => cur?.client?.rfb?.focus());

// The clipboard shared with the guest (the machine's qemu-vdagent channel,
// spice-vdagent in the guest; noVNC's extended clipboard): what the guest
// copies comes here as noVNC's clipboard event and goes to this computer's
// clipboard where the page may write it, else waits behind a button (a
// click lets it); this computer's goes to the guest when the console is
// clicked or the window comes back, where the page may read it without
// asking (Chromium, once allowed), else from the Keys menu.
let lastGuest = "", lastSent = "";

function guestCopied(text) {
  lastGuest = text;
  const show = (on) => { const b = $("#con-clip"); if (b) b.hidden = !on; };
  if (!navigator.clipboard?.writeText) return show(true);
  navigator.clipboard.writeText(text).then(() => show(false), () => show(true));
}

async function copyGuestText() {
  $("#con-clip").hidden = true;
  try {
    await navigator.clipboard.writeText(lastGuest);
    toast(t("已复制客户机的文本"));
  } catch {
    // The browser won't let the page write it (not allowed, or a server
    // reached over plain HTTP): the text, selected, to copy by hand.
    infoDialog(t("客户机复制的文本"), `<textarea rows="7" class="mono guest-text" spellcheck="false" readonly>${esc(lastGuest)}</textarea>
      <p class="hint">${t("浏览器没让这个页面写入剪贴板 (不允许, 或经 HTTP 而不是 HTTPS 打开): 用 Ctrl+C 复制。")}</p>`);
    setTimeout(() => { const ta = document.querySelector("textarea.guest-text"); ta?.focus(); ta?.select(); }, 50);
  }
}

// pushClipboard gives the guest this computer's clipboard when the page may
// read it without a prompt, and it changed.
async function pushClipboard() {
  const rfb = cur?.client?.rfb;
  if (!rfb || !cur.clipboard || !navigator.clipboard?.readText) return;
  try {
    if ((await navigator.permissions.query({ name: "clipboard-read" })).state !== "granted") return;
    const text = await navigator.clipboard.readText();
    if (text && text !== lastSent && text !== lastGuest && cur?.client?.rfb === rfb) {
      lastSent = text;
      rfb.clipboardPasteFrom(text);
    }
  } catch { /* no permission API for it (WebKit): the Keys menu */ }
}
window.addEventListener("focus", () => pushClipboard());

// pasteText puts a text, this computer's clipboard by default, on the
// guest's clipboard, to paste there.
function pasteText() {
  const k = cur, rfb = k.client?.rfb;
  if (!rfb) return;
  formDialog({
    title: t("发送到 {name} 的剪贴板", { name: k.name }),
    body: `<label><span>${t("文本")}</span><textarea name="text" rows="7" class="mono" spellcheck="false"></textarea></label>
      <p class="hint">${t("放进客户机的剪贴板, 再在客户机里粘贴。需要客户机里运行 spice-vdagent; 没有它时用「输入文本…」。")}</p>`,
    ok: t("发送"),
    submit: async (f) => {
      const text = f.text.value;
      if (!text) throw new Error(t("没有文本"));
      lastSent = text;
      rfb.clipboardPasteFrom(text);
      rfb.focus();
      toast(t("已放进 {name} 的剪贴板", { name: k.name }));
    },
  });
  navigator.clipboard?.readText?.().then((s) => { const ta = $("#form-dialog textarea"); if (ta && !ta.value) { ta.value = s; ta.select(); } }).catch(() => {});
}

// clipboardOn gives the machine the clipboard channel (now, if it runs).
async function clipboardOn() {
  const k = cur;
  try {
    showEdited(await call("AddClipboard", k.conn, k.uuid));
    k.clipboard = true;
    menus();
  } catch (e) { fail(e); }
}

// askVNCPassword asks for the password the guest's VNC console wants.
// Cancelling leaves the console disconnected, saying why.
function askVNCPassword(k, rfb, client) {
  conStatus(t("需要 VNC 密码"));
  let sent = false;
  formDialog({
    title: t("{name} 的 VNC 密码", { name: k.name }),
    body: `<label><span>${t("密码")}</span><input type="password" name="password" autocomplete="off" maxlength="64"></label>
      <p class="hint">${t("控制台设置了密码 (设置在虚拟机的「其他设备」里)。它只用来登录 VNC, 不会加密连接。")}</p>`,
    ok: t("登录 VNC"),
    submit: async (f) => {
      if (k.client !== client) return;
      sent = true;
      k.vncPassword = f.password.value;
      conStatus(t("连接中…"));
      rfb.sendCredentials({ password: f.password.value });
    },
  });
  $("#dlg-form").addEventListener("close", () => {
    if (!sent && k.client === client) {
      client.failed = t("需要 VNC 密码");
      rfb.disconnect();
    }
  }, { once: true });
}

// typeText types text into the guest, key by key.
function typeText() {
  const k = cur, rfb = k.client?.rfb;
  if (!rfb) return;
  formDialog({
    title: t("在 {name} 里输入文本", { name: k.name }),
    body: `<label><span>${t("文本")}</span><textarea name="text" rows="7" class="mono" spellcheck="false"></textarea></label>
      <p class="hint">${t("一个键一个键地打进客户机, 按美式键盘布局; 换行是回车。先在客户机里点到要输入的地方。")}</p>`,
    ok: t("输入"),
    submit: async (f) => {
      const { keys, skipped } = typable(f.text.value);
      if (!keys.length) throw new Error(t("没有能用键盘打出的字符"));
      if (keys.length > 20000) throw new Error(t("太长了: 一次最多 20000 个字符"));
      setTimeout(async () => {
        rfb.focus();
        const n = await typeKeys(rfb, keys, (i) => conStatus(t("输入中… {i}/{n}", { i, n: keys.length })), () => cur?.client?.rfb !== rfb);
        if (cur?.client?.rfb === rfb) conStatus(t("已连接"));
        toast(n < keys.length ? t("输入中断: 打了 {n} 个字符", { n })
          : skipped.length ? t("已输入 {n} 个字符; 跳过了键盘打不出的: {chars}", { n, chars: skipped.join(" ") }) : t("已输入 {n} 个字符", { n }), skipped.length > 0);
      }, 0);
    },
  });
  // What the clipboard has, when the page may read it; pasting works too.
  navigator.clipboard?.readText?.().then((s) => { const ta = $("#form-dialog textarea"); if (ta && !ta.value) { ta.value = s; ta.select(); } }).catch(() => {});
}

function conStatus(text, err = false, reconnect = false, force = false) {
  const el = $("#con-status");
  if (!el) return;
  el.textContent = text;
  el.classList.toggle("err", err);
  $("#con-reconnect").hidden = !reconnect;
  $("#con-force").hidden = !force;
}

// endClient drops the console's client and its token, not the view.
function endClient(k) {
  releaseKeys();
  const c = k.client, token = k.token;
  k.client = null;
  k.token = "";
  if (c) c.close();
  if (token) call("CloseConsole", token);
}

async function connect(force = false) {
  const k = cur;
  if (!k) return;
  endClient(k);
  conStatus(t("连接中…"));
  let info;
  try {
    info = k.kind === "serial" ? await call("OpenSerial", k.conn, k.uuid, force) : await call("OpenConsole", k.conn, k.uuid, k.kind);
  } catch (e) {
    conStatus(String(e.message || e), true, true);
    return;
  }
  if (cur !== k || $("#screen")?.className.split(" ")[0] !== info.kind) { // left, or switched, meanwhile
    call("CloseConsole", info.token);
    return;
  }
  k.token = info.token;
  const screen = $("#screen");
  screen.innerHTML = "";
  const lost = (client, clean) => {
    if (k.client !== client) return;
    if (client.failed) return conStatus(client.failed, true, true);
    conStatus(clean ? t("已断开") : t("连接中断"), !clean, true);
  };
  if (info.kind === "serial") {
    const { serialClient } = await import("./serial.js");
    const client = serialClient(screen, wsURL(info.url), {
      onOpen: () => conStatus(t("已连接")),
      // The bridge closes it when the console couldn't open or ended; its
      // last line says which (another client has it: force takes it over).
      onEnd: (clean, tail) => {
        if (k.client !== client) return;
        const taken = /force takes it over/.test(tail);
        conStatus(taken ? t("另一个客户端正在使用这个串口") : clean ? t("已断开") : t("串口已结束"), !clean, true, taken);
      },
    });
    k.client = Object.assign(client, { cad: () => {} }); // no Ctrl+Alt+Del on a serial line
  } else if (info.kind === "vnc") {
    const { default: RFB } = await import("./vendor/novnc/core/rfb.js");
    const rfb = new RFB(screen, wsURL(info.url), { wsProtocols: ["binary"] });
    scale(rfb);
    rfb.resizeSession = resize();
    rfb.background = "#000";
    const client = { rfb, cad: () => rfb.sendCtrlAltDel(), close: () => rfb.disconnect() };
    rfb.addEventListener("connect", () => { conStatus(t("已连接")); rfb.focus(); checkResize(rfb); pushClipboard(); });
    rfb.addEventListener("clipboard", (e) => { if (k.client === client && k.clipboard) guestCopied(e.detail.text); });
    screen.addEventListener("pointerdown", () => pushClipboard());
    rfb.addEventListener("disconnect", (e) => lost(client, e.detail.clean));
    // A console with a password: asked here, kept for this console's
    // reconnections (in memory only), asked again when it was wrong.
    rfb.addEventListener("credentialsrequired", () => {
      if (k.client !== client) return;
      if (k.vncPassword) return rfb.sendCredentials({ password: k.vncPassword });
      askVNCPassword(k, rfb, client);
    });
    rfb.addEventListener("securityfailure", (e) => {
      if (k.client !== client) return;
      client.failed = k.vncPassword ? t("VNC 密码不对; 重新连接再输入") : t("VNC 登录失败: {reason}", { reason: e.detail.reason || e.detail.status });
      k.vncPassword = "";
    });
    k.client = client;
  } else {
    const S = await import("./vendor/spice/main.js");
    screen.innerHTML = `<div id="spice-screen"></div>`;
    let sc;
    const client = { cad: () => S.sendCtrlAltDel(sc), close: () => sc.stop() };
    sc = new S.SpiceMainConn({
      uri: wsURL(info.url), screen_id: "spice-screen",
      onsuccess: () => conStatus(t("已连接")),
      onerror: () => lost(client, false),
    });
    k.client = client;
  }
}

export function closeConsole() {
  const k = cur;
  if (!k) return;
  cur = null;
  endClient(k);
  if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
  const main = $("#main");
  main.dataset.view = "";
  main.classList.remove("console");
}
