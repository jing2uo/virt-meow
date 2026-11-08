// The details page's live charts: CPU, memory, disk and network of the
// machine shown, from the host's Stats every 2 s while the tab is open.
import { t } from "./i18n.js";
import { $, esc, errText, kib } from "./util.js";
import { addTab, off, shownMachine } from "./machine.js";
import { watch, tick, chart, rate, machineHistory, EVERY } from "./monitor.js";

addTab({
  id: "monitor",
  label: () => t("监控"),
  html: (k) => off(k.info.state)
    ? `<p class="mute">${t("开机后显示它的 CPU、内存、磁盘和网络。")}</p>`
    : `<section><div class="cards charts" id="mon">${draw(k)}</div>
      <p class="hint" id="mon-note">${esc(t("每 {s} 秒更新; 最近 3 分钟。", { s: EVERY / 1000 }))}</p></section>`,
  wire: (k) => { if (!off(k.info.state)) tick(); },
});

function draw(k) {
  const h = machineHistory(k.conn, k.uuid), p = h.at(-1) || {};
  const col = (f) => h.map(f);
  const card = (title, now, svg, legend = "") => `<div class="card"><h3>${esc(title)} <span class="now">${now}</span></h3>${svg}${legend}</div>`;
  const legend = (a, b) => `<p class="legend"><span class="a">${esc(a)}</span><span class="b">${esc(b)}</span></p>`;
  return card("CPU", p.cpu == null ? "–" : `${p.cpu.toFixed(1)}%`, chart([{ values: col((x) => x.cpu), cls: "a" }], 100))
    + card(t("内存"), p.used ? esc(`${kib(p.used)} / ${kib(p.total)}`) : "–",
      chart([{ values: col((x) => x.used), cls: "a" }], p.total || undefined))
    + card(t("磁盘"), `${esc(rate(p.rd))} / ${esc(rate(p.wr))}`,
      chart([{ values: col((x) => x.rd), cls: "a" }, { values: col((x) => x.wr), cls: "b" }]), legend(t("读"), t("写")))
    + card(t("网络"), `${esc(rate(p.rx))} / ${esc(rate(p.tx))}`,
      chart([{ values: col((x) => x.rx), cls: "a" }, { values: col((x) => x.tx), cls: "b" }]), legend(t("收"), t("发")));
}

watch({
  kind: "Stats",
  conn: () => {
    const k = shownMachine();
    return k && k.tab === "monitor" && $("#mon") ? k.conn : null;
  },
  show: (d, err) => {
    const k = shownMachine(), el = $("#mon");
    if (!k || !el) return;
    const note = $("#mon-note");
    note.classList.toggle("err", !!err);
    if (err) { note.textContent = t("读不到统计: {err}", { err: errText(err) }); return; }
    if (!d.machines.some((m) => m.uuid === k.uuid)) {
      note.textContent = t("它没有在运行。");
      return;
    }
    el.innerHTML = draw(k);
    note.textContent = t("每 {s} 秒更新; 最近 3 分钟。", { s: EVERY / 1000 });
  },
});
