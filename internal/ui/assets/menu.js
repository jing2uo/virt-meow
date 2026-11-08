// "More" menus: a button and the actions behind it, for a header or a
// table's row that has room for only a few. The menu is fixed to the
// window when open, so a table that scrolls sideways doesn't clip it; it
// closes on a click elsewhere, Esc, a scroll or a resize.
import { t } from "./i18n.js";
import { esc, icon } from "./util.js";

// moreMenu draws a menu of items: { a, label, cls, disabled, title, data,
// checked } (data: more data-* attributes, as { i: 3 }; checked, when
// given, makes it an item that is on or off), or "-" for a separator.
// Each item is a button with data-a, as a toolbar's would be, so the
// view wires both the same way.
// label, when given, is shown with a down arrow; else the button is an
// icon, named by title for screen readers.
export function moreMenu(items, { label = "", title = t("更多操作"), icon: ic = "" } = {}) {
  const list = items.filter((x, i, all) => x !== "-" || (i > 0 && i < all.length - 1 && all[i - 1] !== "-"));
  if (!list.length) return "";
  const face = label ? `${ic ? icon(ic) : ""}<span class="lbl">${esc(label)}</span>${icon("chevron-down")}` : icon("ellipsis");
  return `<span class="more"><button type="button" class="more-btn${label ? "" : " icon-only"}" aria-haspopup="menu" aria-expanded="false" aria-label="${esc(title)}" title="${esc(title)}">${face}</button><span class="menu" role="menu" hidden>${list.map(menuItem).join("")}</span></span>`;
}

// actionIcons: the icon of each action a menu or button shows.
export const actionIcons = {
  start: "play", resume: "play", shutdown: "power", reboot: "rotate-cw", pause: "pause", "force-off": "power-off",
  reset: "rotate-ccw", console: "monitor", delete: "trash-2", remove: "trash-2", xml: "file-code", clone: "copy",
  rename: "pencil", save: "save", reload: "refresh-cw", download: "download", grow: "hard-drive", autostart: "power",
  stop: "power", link: "cable", revert: "history",
};

// button draws an action as a button: its icon and label.
export function actionButton(x, cls = "") {
  const c = [x.cls, cls].filter(Boolean).join(" ");
  return `<button type="button" data-a="${esc(x.a)}"${Object.entries(x.data || {}).map(([k, v]) => ` data-${k}="${esc(v)}"`).join("")}${c ? ` class="${esc(c)}"` : ""}${x.disabled ? " disabled" : ""}${x.title ? ` title="${esc(x.title)}"` : ""}>${x.icon ?? actionIcons[x.a] ? icon(x.icon ?? actionIcons[x.a]) : ""}${esc(x.label)}</button>`;
}

function menuItem(x) {
  if (x === "-") return `<span class="menu-sep" role="separator"></span>`;
  const check = x.checked !== undefined;
  const cls = [x.cls, check ? "check" : ""].filter(Boolean).join(" ");
  const data = Object.entries(x.data || {}).map(([k, v]) => ` data-${k}="${esc(v)}"`).join("");
  const ic = check ? "" : x.icon ?? actionIcons[x.a] ?? "";
  return `<button type="button" role="${check ? "menuitemcheckbox" : "menuitem"}"${check ? ` aria-checked="${!!x.checked}"` : ""} data-a="${esc(x.a)}"${data}${cls ? ` class="${esc(cls)}"` : ""}${x.disabled ? " disabled" : ""}${x.title ? ` title="${esc(x.title)}"` : ""}>${ic ? icon(ic) : `<span class="i blank" aria-hidden="true"></span>`}${esc(x.label)}</button>`;
}

// machineActions splits what a machine can do now (d.actions, hv's power
// actions for its state) into the few shown, start or resume, shut down
// and the console, and the rest behind "more": the other power actions,
// then mid (the page's own: XML, clone…), then apart and last the ones
// that lose something, force off, reset and delete.
export function machineActions(d, { console: con = null, mid = [] } = {}) {
  const has = (a) => d.actions.includes(a);
  const item = (a, cls = "") => ({ a, label: t(a), cls });
  const shown = [];
  if (has("start")) shown.push(item("start", "primary"));
  if (has("resume")) shown.push(item("resume", "primary"));
  if (has("shutdown")) shown.push(item("shutdown"));
  if (con) shown.push({ a: "console", label: t("控制台"), ...con });
  const more = [...["reboot", "pause"].filter(has).map((a) => item(a)), "-", ...mid, "-",
    ...["force-off", "reset"].filter(has).map((a) => item(a, "del")), { a: "delete", label: t("删除"), cls: "del" }];
  return { shown, more };
}

// rowActions are a machine's actions in a list's row: the few that fit
// its state, the rest behind "more".
export function rowActions(d) {
  const { shown, more } = machineActions(d, {
    console: d.state === "shutoff" || d.state === "crashed" ? null : {},
    mid: [{ a: "xml", label: t("编辑 XML") }],
  });
  // A list's rows are many: no loud primary buttons in them.
  return `<div>${shown.map((x) => actionButton({ ...x, cls: "" })).join("")}${moreMenu(more)}</div>`;
}

let open = null; // { btn, menu }

export const menuOpen = () => !!open;

function close(focus = false) {
  if (!open) return;
  const { btn, menu } = open;
  open = null;
  menu.hidden = true;
  btn.setAttribute("aria-expanded", "false");
  if (focus && btn.isConnected) btn.focus();
}

function show(btn) {
  const menu = btn.nextElementSibling;
  open = { btn, menu };
  menu.hidden = false;
  btn.setAttribute("aria-expanded", "true");
  const r = btn.getBoundingClientRect(), w = menu.offsetWidth, h = menu.offsetHeight;
  menu.style.left = Math.max(8, Math.min(r.right - w, innerWidth - w - 8)) + "px";
  menu.style.top = (r.bottom + 4 + h > innerHeight - 8 && r.top - 4 - h > 8 ? r.top - 4 - h : r.bottom + 4) + "px";
  menu.querySelector("button:not(:disabled)")?.focus();
}

// Capture: before a view's own handlers, which may redraw the button away.
document.addEventListener("click", (e) => {
  const btn = e.target.closest(".more-btn");
  if (btn) {
    e.stopPropagation(); // the row under it doesn't open
    const was = open?.btn === btn;
    close();
    if (!was) show(btn);
    return;
  }
  if (open && !open.menu.contains(e.target)) close();
}, true);
// An item's own handler runs first, then the menu goes.
document.addEventListener("click", (e) => { if (open && open.menu.contains(e.target) && e.target.closest("button")) close(); });
document.addEventListener("keydown", (e) => {
  if (!open) return;
  if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); close(true); return; }
  if (e.key === "ArrowDown" || e.key === "ArrowUp") {
    e.preventDefault();
    const items = [...open.menu.querySelectorAll("button:not(:disabled)")];
    const i = items.indexOf(document.activeElement);
    items[(i + (e.key === "ArrowDown" ? 1 : -1) + items.length) % items.length]?.focus();
  }
  if (e.key === "Tab") close();
}, true);
addEventListener("resize", () => close());
document.addEventListener("scroll", (e) => { if (open && !open.menu.contains(e.target)) close(); }, true);
