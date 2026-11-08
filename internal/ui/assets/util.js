// What every view of the page uses: the DOM helpers, toasts, the confirm
// and form dialogs, and how sizes and edits read.
import { t } from "./i18n.js";

export const $ = (s, root = document) => root.querySelector(s);
export const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
export const errText = (e) => String((e && e.message) || e).trim();

// icon is one of the vendored Lucide icons (style.css lists them), hidden
// from screen readers: what it is on says what it does.
export const icon = (name, cls = "") => `<span class="i i-${name}${cls ? " " + cls : ""}" aria-hidden="true"></span>`;

// errorCard is a failure where a view would be: what failed, and libvirt's
// message, selectable.
export const errorCard = (title, msg) => `<div class="card err" role="alert"><h3>${icon("circle-alert")}${esc(title)}</h3>${msg ? `<p class="msg">${esc(msg)}</p>` : ""}</div>`;

// emptyState says there is nothing here and what to do about it; button,
// when given, is { id, label, icon }. small sits inside a section.
export function emptyState({ icon: ic = "inbox", title, text = "", button = null, small = false }) {
  return `<div class="empty${small ? " small" : ""}">${icon(ic, "i-xl")}<h2>${esc(title)}</h2>${text ? `<p>${esc(text)}</p>` : ""}
    ${button ? `<div class="buttons"><button type="button" class="primary" id="${esc(button.id)}">${button.icon ? icon(button.icon) : ""}${esc(button.label)}</button></div>` : ""}</div>`;
}

// skeleton stands where something is being read: grey bars, not a blank.
export const skeleton = (rows = false) => `<div class="skeleton${rows ? " rows" : ""}" role="status" aria-label="${esc(t("读取中…"))}"><span></span><span></span><span></span></div>`;

// toast says how something went, for a while (longer for a failure, and
// as long as the pointer is on it); its button opens the activity panel,
// where it stays.
export function toast(msg, err = false) {
  const el = document.createElement("div");
  el.className = "toast" + (err ? " err" : "");
  el.setAttribute("role", err ? "alert" : "status");
  el.innerHTML = `${icon(err ? "circle-alert" : "circle-check")}<div class="body"><div class="text"></div>
    <div class="acts"><button type="button" class="small">${icon("activity")}${esc(t("在动态里查看"))}</button></div></div>
    <button type="button" class="icon close" aria-label="${esc(t("关闭"))}">${icon("x")}</button>`;
  el.querySelector(".text").textContent = msg;
  el.querySelector(".acts button").onclick = () => { el.remove(); document.dispatchEvent(new CustomEvent("virt-mm:activity")); };
  el.querySelector(".close").onclick = () => el.remove();
  $("#toasts").append(el);
  let timer;
  const later = () => { timer = setTimeout(() => el.remove(), err ? 9000 : 4500); };
  el.onmouseenter = () => clearTimeout(timer);
  el.onmouseleave = later;
  later();
  return el;
}
export const fail = (e) => toast(errText(e), true);

export function kib(n) {
  if (!n) return "–";
  if (n % (1 << 20) === 0) return `${n >> 20} GiB`;
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} GiB`;
  return `${Math.round(n / 1024)} MiB`;
}

export const gib = (bytes) => bytes < 1 << 30
  ? Math.max(1, Math.round(bytes / (1 << 20))) + " MiB"
  : (bytes / (1 << 30)).toFixed(1).replace(/\.0$/, "") + " GiB";

export function confirmDanger(title, text, ok = title) {
  const dlg = $("#dlg-confirm");
  $("#confirm-title").textContent = title;
  $("#confirm-text").textContent = text;
  $("#confirm-ok").textContent = ok;
  dlg.returnValue = "";
  dlg.showModal();
  return new Promise((resolve) => dlg.addEventListener("close", () => resolve(dlg.returnValue === "ok"), { once: true }));
}

// formDialog shows a small form: body is its fields' HTML; submit gets the
// form's elements and closes the dialog when it resolves, or leaves the
// error under the fields when it throws. busy is the button's label
// meanwhile.
//
// A long submit (runOp) goes on when the dialog is closed meanwhile: its
// failure is then a toast, and its end leaves alone the dialog shown by
// then.
let formShown = 0;
export function formDialog({ title, body, ok, busy = "", danger = false, submit }) {
  const dlg = $("#dlg-form"), f = $("#form-dialog"), mine = ++formShown;
  f.innerHTML = `<h2>${esc(title)}</h2>${body}<p class="error" id="form-error"></p>
    <div class="buttons"><button type="button" id="form-cancel">${t("取消")}</button>
      <button type="submit" class="${danger ? "danger" : "primary"}" id="form-ok">${esc(ok)}</button></div>`;
  $("#form-cancel").onclick = () => dlg.close("cancel");
  f.onsubmit = async (e) => {
    e.preventDefault();
    const btn = $("#form-ok"), here = () => formShown === mine && dlg.open;
    btn.disabled = true;
    btn.classList.add("busy");
    if (busy) btn.innerHTML = `${icon("loader-circle", "spin")}${esc(busy)}`;
    $("#form-error").textContent = "";
    try {
      await submit(f.elements);
      if (here()) dlg.close("ok");
    } catch (err) {
      if (here()) $("#form-error").textContent = errText(err);
      else toast(errText(err), true);
    }
    if (formShown !== mine) return;
    btn.disabled = false;
    btn.classList.remove("busy");
    btn.textContent = ok;
  };
  dlg.returnValue = "";
  dlg.showModal();
  const first = f.querySelector("input, select, textarea");
  if (first) { first.focus(); first.select?.(); }
}

// infoDialog shows html under a title, with Close.
export function infoDialog(title, html) {
  const dlg = $("#dlg-form"), f = $("#form-dialog");
  formShown++;
  f.innerHTML = `<h2>${esc(title)}</h2>${html}<div class="buttons"><button type="submit" class="primary" id="form-ok">${t("关闭")}</button></div>`;
  f.onsubmit = (e) => { e.preventDefault(); dlg.close("ok"); };
  dlg.returnValue = "";
  dlg.showModal();
  $("#form-ok").focus();
}

// edited says what an edit (hv.Edited) did: on a running machine, what
// took effect now and what waits for its next start.
export function edited(e) {
  if (!e.changes.length) return t("{name}: 没有变化", { name: e.name });
  const lines = e.changes.map((c) => "· " + c.what + (!e.active || c.live ? ""
    : c.note ? " " + t("(下次启动时生效: {note})", { note: c.note }) : " " + t("(下次启动时生效)")));
  return [t("已修改 {name}:", { name: e.name }), ...lines, ...(e.warnings || []).map((w) => "⚠ " + w)].join("\n");
}

// showEdited toasts an edit's outcome, longer when it has warnings.
export function showEdited(e) {
  toast(edited(e), !!(e.warnings && e.warnings.length));
}
