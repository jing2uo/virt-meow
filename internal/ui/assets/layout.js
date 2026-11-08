// The page's frame at any window size: the sidebar, which sits beside the
// page when the window is wide (unless put away, which is remembered) and
// is a drawer over it when narrow; and tab bars, which scroll sideways
// with arrows when their tabs don't fit, and are a select when narrow.
import { t } from "./i18n.js";
import { $, esc, icon } from "./util.js";

// ---- the sidebar ------------------------------------------------------------

// hidden: a wide window's sidebar is put away. The settings (the config
// dir) say it once they are read; the page's own copy meanwhile, so it
// doesn't flash. saveSide keeps a change.
const KEY = "virt-mm.sidebar";
const narrow = matchMedia("(max-width: 899px)");
let drawer = false; // narrow: the drawer is out
let hidden = localStorage.getItem(KEY) === "closed";
let saveSide = () => {};

export function initSide(isHidden, save) {
  hidden = isHidden;
  saveSide = save;
  localStorage.setItem(KEY, hidden ? "closed" : "open");
  sync();
}

function sync() {
  const closed = narrow.matches ? !drawer : hidden;
  document.body.classList.toggle("side-closed", closed);
  document.body.classList.toggle("side-drawer", narrow.matches && drawer);
  $("#side-hide")?.setAttribute("aria-label", t("收起侧栏"));
}

// sideButton is the button that brings the sidebar back, for a view's
// header; it shows only while the sidebar is away.
export const sideButton = () => `<button type="button" class="icon side-toggle" data-side aria-label="${esc(t("显示侧栏"))}" title="${esc(t("显示侧栏"))}">${icon("menu", "i-lg")}</button>`;

export function toggleSide(show) {
  if (narrow.matches) drawer = show ?? !drawer;
  else {
    hidden = !(show ?? hidden);
    localStorage.setItem(KEY, hidden ? "closed" : "open");
    saveSide(hidden);
  }
  sync();
  if (!document.body.classList.contains("side-closed")) $("aside .conn.sel, aside button")?.focus({ preventScroll: true });
}

// closeDrawer puts the drawer away once something in it was picked.
export function closeDrawer() {
  if (!drawer) return;
  drawer = false;
  sync();
}

document.addEventListener("click", (e) => {
  if (e.target.closest("[data-side]")) toggleSide();
  else if (e.target.id === "side-scrim") closeDrawer();
});
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && drawer && !document.querySelector("dialog[open]")) { e.preventDefault(); closeDrawer(); }
});
narrow.addEventListener("change", () => { drawer = false; sync(); });
sync();

// ---- tab bars ---------------------------------------------------------------

// tabBar draws tabs, [[key, label, icon?]], with on the current one: buttons that
// scroll sideways between two arrows when they don't fit, and a select
// in their place when the window is narrow.
export function tabBar(id, tabs, on) {
  return `<div class="tabbar" id="${esc(id)}">
    <button type="button" class="tab-arrow prev" tabindex="-1" aria-hidden="true">${icon("chevron-left")}</button>
    <nav class="tabs" role="tablist">${tabs.map(([k, label, ic]) => `<button type="button" role="tab" data-tab="${esc(k)}" aria-selected="${k === on}" class="${k === on ? "on" : ""}">${ic ? icon(ic) : ""}${esc(label)}</button>`).join("")}</nav>
    <button type="button" class="tab-arrow next" tabindex="-1" aria-hidden="true">${icon("chevron-right")}</button>
    <select class="tab-select" aria-label="${esc(t("视图"))}">${tabs.map(([k, label]) => `<option value="${esc(k)}"${k === on ? " selected" : ""}>${esc(label)}</option>`).join("")}</select>
  </div>`;
}

// One observer for every tab bar on screen; those drawn away are dropped.
const hints = new Map(); // nav -> its hint
const ro = new ResizeObserver((es) => { for (const e of es) hints.get(e.target)?.(); });

// wireTabs calls pick(key) when a tab is chosen, and keeps the arrows
// showing where there are more tabs.
export function wireTabs(el, pick) {
  for (const n of hints.keys()) if (!n.isConnected) { ro.unobserve(n); hints.delete(n); }
  const nav = el.querySelector(".tabs");
  const hint = () => {
    el.classList.toggle("more-left", nav.scrollLeft > 2);
    el.classList.toggle("more-right", nav.scrollLeft + nav.clientWidth < nav.scrollWidth - 2);
  };
  nav.onclick = (e) => { const b = e.target.closest("button"); if (b && !b.classList.contains("on")) pick(b.dataset.tab); };
  el.querySelector(".tab-select").onchange = (e) => pick(e.target.value);
  el.querySelector(".prev").onclick = () => nav.scrollBy({ left: -nav.clientWidth * 0.7, behavior: "smooth" });
  el.querySelector(".next").onclick = () => nav.scrollBy({ left: nav.clientWidth * 0.7, behavior: "smooth" });
  nav.onscroll = hint;
  hints.set(nav, hint);
  ro.observe(nav);
  const on = nav.querySelector(".on");
  if (on) nav.scrollLeft = Math.max(0, on.offsetLeft - nav.offsetLeft - nav.clientWidth / 2 + on.offsetWidth / 2);
  hint();
}
