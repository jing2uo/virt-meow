// Live statistics: the views that show them say so (watch), and every 2 s,
// while the page is visible, each connection they show is asked once
// (Stats, HostStats); the server samples a host once however many pages ask.
// What came back is kept for a few minutes, for the charts.
import { call } from "./rpc.js";
import { esc } from "./util.js";

export const EVERY = 2000;
const KEEP = 90; // points: 3 minutes

// watchers: { kind: "Stats" | "HostStats", conn: () => id, ids (the
// overview's hosts) or null when not shown, show: (data, err, id) }.
const watchers = [];
export function watch(w) { watchers.push(w); }

// history: "conn/uuid" (a machine) or "conn" (the host) -> points.
const history = new Map();
const last = new Map(); // conn -> the last Stats, by machine UUID

export const machineHistory = (conn, uuid) => history.get(conn + "/" + uuid) || [];
export const hostHistory = (conn) => history.get(conn) || [];
export const lastStats = (conn, uuid) => last.get(conn)?.get(uuid);

function push(key, p) {
  let h = history.get(key);
  if (!h) history.set(key, (h = []));
  h.push(p);
  if (h.length > KEEP) h.splice(0, h.length - KEEP);
}

// used is a running machine's memory in use: what the guest reports
// through its balloon driver, else what its process takes on the host.
export const memUsed = (m) => m.guest_used_kib || m.rss_kib || 0;

function record(kind, conn, d) {
  const t = Date.parse(d.time) || Date.now();
  if (kind === "HostStats") {
    push(conn, { t, cpu: d.cpu_percent, used: d.used_kib, total: d.memory_kib });
    return;
  }
  const byUUID = new Map();
  for (const m of d.machines) {
    byUUID.set(m.uuid, m);
    push(conn + "/" + m.uuid, { t, cpu: m.new ? null : m.cpu_percent, used: memUsed(m), total: m.memory_kib,
      rd: m.new ? null : m.disk_read_bps, wr: m.new ? null : m.disk_write_bps, rx: m.new ? null : m.net_rx_bps, tx: m.new ? null : m.net_tx_bps });
  }
  last.set(conn, byUUID);
  // Machines no longer running: their charts start again when they do.
  for (const k of history.keys()) if (k.startsWith(conn + "/") && !byUUID.has(k.slice(conn.length + 1))) history.delete(k);
}

let busy = false;
export async function tick() {
  if (busy || document.hidden) return;
  busy = true;
  const want = new Map(); // "kind conn" -> watchers
  for (const w of watchers) {
    for (const c of [].concat(w.conn() || [])) {
      const k = w.kind + " " + c;
      if (!want.has(k)) want.set(k, []);
      want.get(k).push(w);
    }
  }
  await Promise.all([...want].map(async ([k, ws]) => {
    const [kind, conn] = k.split(" ");
    let d, err = null;
    try { d = await call(kind, conn); record(kind, conn, d); } catch (e) { err = e; }
    for (const w of ws) {
      try { w.show(d, err, conn); } catch (e) { console.error(e); }
    }
  }));
  busy = false;
}
setInterval(tick, EVERY);
document.addEventListener("visibilitychange", () => { if (!document.hidden) tick(); });

// rate reads bytes per second.
export function rate(bps) {
  if (bps == null) return "–";
  const u = ["B/s", "KiB/s", "MiB/s", "GiB/s"];
  let i = 0;
  while (bps >= 1024 && i < u.length - 1) { bps /= 1024; i++; }
  return (i === 0 ? Math.round(bps) : bps.toFixed(bps < 10 ? 1 : 0)) + " " + u[i];
}

// chart draws series of points (null: no value) as lines in an SVG, the
// first filled; max is the top of the scale, or found from the values.
export function chart(series, max) {
  const W = 300, H = 80, n = KEEP;
  const top = max || Math.max(1, ...series.flatMap((s) => s.values.filter((v) => v != null))) * 1.15;
  const line = (vals) => {
    const off = n - vals.length;
    const pts = [];
    vals.forEach((v, i) => { if (v != null) pts.push(`${(((off + i) / (n - 1)) * W).toFixed(1)},${(H - (Math.min(v, top) / top) * H).toFixed(1)}`); });
    return pts;
  };
  const paths = series.map((s, i) => {
    const pts = line(s.values);
    if (pts.length === 0) return "";
    const d = "M" + pts.join(" L");
    const area = i === 0 && pts.length > 1 ? `<path class="area ${esc(s.cls)}" d="${d} L${pts.at(-1).split(",")[0]},${H} L${pts[0].split(",")[0]},${H} Z"/>` : "";
    return `${area}<path class="line ${esc(s.cls)}" d="${d}"/>`;
  }).join("");
  return `<svg class="chart" viewBox="0 0 ${W} ${H}" preserveAspectRatio="none" aria-hidden="true">
    <line class="grid" x1="0" y1="${H / 2}" x2="${W}" y2="${H / 2}"/>${paths}</svg>`;
}
