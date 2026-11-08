// Long operations (a machine created or cloned, a snapshot taken or gone
// back to, a disk grown, a machine's state saved): each shows while it
// runs, over the page beside the uploads, saying what it does to what;
// it can't be started twice; and it ends in a toast (util.js) that opens
// the activity panel. They keep running when the dialog that started one
// is closed.
import { t } from "./i18n.js";
import { $, esc, icon } from "./util.js";

// running: key -> what it does. A key names what can't run twice at once:
// a machine ("conn/uuid"), whose jobs libvirt runs one at a time anyway.
const running = new Map();
const listeners = new Set();

// busy is what runs on key now, "" when nothing does.
export const busy = (key) => running.get(key)?.label || "";

// onOps calls fn whenever an operation starts or ends.
export function onOps(fn) { listeners.add(fn); }

// onOpDone calls fn(label, err) when one ends (err null when it worked).
const done = new Set();
export function onOpDone(fn) { done.add(fn); }

// runOp runs fn as the operation label on key, and returns what it does;
// it throws, without running fn, when key is busy.
export async function runOp(key, label, fn) {
  if (running.has(key)) throw new Error(t("{what} 还没完成, 等它结束再试", { what: running.get(key).label }));
  const el = document.createElement("div");
  el.className = "upload op";
  el.setAttribute("role", "status");
  el.innerHTML = `<div class="top">${icon("loader-circle", "spin")}<span>${esc(label)}…</span></div>`;
  $("#uploads").append(el);
  running.set(key, { label, el });
  changed();
  let err = null;
  try {
    return await fn();
  } catch (e) {
    err = e;
    throw e;
  } finally {
    running.delete(key);
    el.remove();
    changed();
    for (const f of done) {
      try { f(label, err); } catch (e) { console.error(e); }
    }
  }
}

function changed() {
  for (const fn of listeners) {
    try { fn(); } catch (e) { console.error(e); }
  }
}
