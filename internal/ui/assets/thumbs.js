// Small pictures of the running machines in the list: every few seconds,
// only for the rows on screen, a few at a time (api.Thumbnail: the screen
// scaled down on the host's side of virt-meow). Each costs a screenshot, so
// they are off by default for a remote host with many machines running,
// and go off by themselves when they come back slowly; a toggle per
// connection says what the user wants.
import { call } from "./rpc.js";
import { t } from "./i18n.js";
import { esc } from "./util.js";

const EVERY = 5000, WIDTH = 192, AT_ONCE = 3, SLOW = 3000, MANY = 8;

const pics = new Map(); // "conn/uuid" -> data URL
const none = new Map(); // "conn/uuid" -> why it has no picture (no display, …)
const slow = new Set(); // connections whose pictures went off by themselves
const visible = new Set(); // td elements on screen
let current = () => null; // { conn, remote, doms } of the list shown, or null

const key = (conn) => "virt-mm.thumbs." + conn;

// never: the settings turned them off everywhere (no switch in the list).
let never = false;
export function setThumbsNever(off) { never = off; }
export const thumbsNever = () => never;

// thumbsOn: the user's choice for the connection, else on unless the host
// is remote with many machines running; off once they were too slow.
export function thumbsOn(conn, remote, running) {
  if (never) return false;
  const v = localStorage.getItem(key(conn));
  if (v) return v === "on";
  if (slow.has(conn)) return false;
  return !remote || running <= MANY;
}

export function setThumbs(conn, on) {
  localStorage.setItem(key(conn), on ? "on" : "off");
  slow.delete(conn);
}

export const thumbsSlow = (conn) => slow.has(conn) && !localStorage.getItem(key(conn));

// thumbCell is a row's picture cell: the last picture, or a placeholder.
export function thumbCell(conn, d) {
  return `<td class="thumb" data-thumb="${esc(d.uuid)}">${d.state === "running" ? pic(conn + "/" + d.uuid) : `<span class="pic"></span>`}</td>`;
}

function pic(k) {
  if (pics.has(k)) return `<img src="${pics.get(k)}" alt="">`;
  if (none.has(k)) return `<span class="pic none" title="${esc(none.get(k))}">${t("无画面")}</span>`;
  return `<span class="pic wait"></span>`;
}

const seen = new IntersectionObserver((es) => {
  for (const e of es) {
    if (e.isIntersecting) visible.add(e.target);
    else visible.delete(e.target);
  }
});

// watchThumbs follows the picture cells of a list just drawn; list says
// which list is shown.
export function watchThumbs(root, list) {
  current = list;
  seen.disconnect();
  visible.clear();
  root.querySelectorAll("td.thumb").forEach((td) => seen.observe(td));
}

let busy = false;
async function tick() {
  const l = current();
  if (busy || document.hidden || !l) return;
  const running = l.doms.filter((d) => d.state === "running");
  if (!thumbsOn(l.conn, l.remote, running.length)) return;
  const want = [...visible].filter((td) => td.isConnected).map((td) => running.find((d) => d.uuid === td.dataset.thumb)).filter(Boolean);
  if (!want.length) return;
  busy = true;
  let took = 0;
  for (let i = 0; i < want.length; i += AT_ONCE) {
    await Promise.all(want.slice(i, i + AT_ONCE).map(async (d) => {
      const t0 = performance.now();
      const k = l.conn + "/" + d.uuid;
      try {
        const s = await call("Thumbnail", l.conn, d.uuid, WIDTH);
        pics.set(k, "data:image/png;base64," + s.png);
        none.delete(k);
      } catch (e) {
        // No display, or stopping meanwhile (the row says so): no picture.
        pics.delete(k);
        none.set(k, String(e.message || e));
      }
      const td = document.querySelector(`td.thumb[data-thumb="${CSS.escape(d.uuid)}"]`);
      if (td) td.innerHTML = pic(k);
      took = Math.max(took, performance.now() - t0);
    }));
  }
  busy = false;
  if (took > SLOW && !localStorage.getItem(key(l.conn))) {
    slow.add(l.conn);
    onSlow(l.conn);
  }
}
let onSlow = () => {};
export function onThumbsSlow(fn) { onSlow = fn; }

setInterval(tick, EVERY);
export const thumbsNow = () => setTimeout(tick, 50);

// thumbsToggle is the switch above the list.
export function thumbsToggle(conn, on) {
  return `<label class="switch thumbs-toggle" title="${esc(t("每 {s} 秒给屏幕上运行中的虚拟机截一张小图; 每张都要在主机上截屏。", { s: EVERY / 1000 }))}">
    <input type="checkbox" id="thumbs-on"${on ? " checked" : ""}><span>${t("显示画面")}</span></label>
    ${thumbsSlow(conn) ? `<small class="mute">${t("主机回得太慢, 画面已自动关闭")}</small>` : ""}`;
}
