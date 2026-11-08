// The keyboard everywhere: what is clicked can be reached with Tab and
// pressed with Enter or Space (rows that pick something, the sidebar's
// connections, a host's card); a modal dialog keeps Tab inside it and,
// closed, gives focus back to where it was before.

const focusable = 'a[href], button:not(:disabled), input:not(:disabled):not([type=hidden]), select:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])';

// Enter or Space on something that acts as a button clicks it.
document.addEventListener("keydown", (e) => {
  if (e.key !== "Enter" && e.key !== " ") return;
  const el = e.target instanceof Element ? e.target : null;
  if (!el || !el.matches('[role="button"], tr.open[tabindex]')) return;
  e.preventDefault();
  el.click();
});

// before is what had focus outside every dialog last: where a dialog's
// focus goes back to.
let before = null;
document.addEventListener("focusin", (e) => {
  if (!(e.target instanceof Element) || e.target.closest("dialog")) return;
  before = e.target;
});

// A dialog closed: focus back where it was, or, when that is gone or
// hidden (a menu's item), on what opened it (the menu's button).
document.addEventListener("close", (e) => {
  if (!(e.target instanceof HTMLDialogElement) || document.querySelector("dialog[open]")) return;
  let el = before;
  if (el && (!el.isConnected || el.closest("[hidden]"))) el = el.closest(".more")?.querySelector(".more-btn") || null;
  if (el && el.isConnected && !el.closest("[hidden]")) el.focus({ preventScroll: true });
}, true);

// Tab stays in the dialog shown, going round from its last field to its
// first.
document.addEventListener("keydown", (e) => {
  if (e.key !== "Tab") return;
  const dlgs = [...document.querySelectorAll("dialog[open]")];
  const dlg = dlgs[dlgs.length - 1];
  if (!dlg) return;
  const items = [...dlg.querySelectorAll(focusable)].filter((x) => x.offsetParent !== null || x === document.activeElement);
  if (!items.length) return;
  const first = items[0], last = items[items.length - 1];
  if (!dlg.contains(document.activeElement)) { e.preventDefault(); first.focus(); return; }
  if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
  else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
});
