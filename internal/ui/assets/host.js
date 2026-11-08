// A connection's host tab: what the host is (virt-mm host), its CPU and
// memory in use as they go (HostStats every 2 s while shown), and its PCI
// and USB devices with the machines using them (virt-mm hostdevs).
import { call } from "./rpc.js";
import { t } from "./i18n.js";
import { $, esc, fail, errText, kib, icon, errorCard, skeleton, emptyState } from "./util.js";
import { watch, tick, chart, hostHistory, EVERY } from "./monitor.js";

// What the host is and its devices change rarely: read when the tab is
// drawn if older than this, or on Refresh. The page redraws the tab on
// every machine event.
const FRESH = 60_000;
const cache = new Map(); // conn -> { host, devs, at, err, devErr }
let shown = null; // the connection whose host tab is drawn

export function renderHost(body, conn) {
  shown = conn;
  let c = cache.get(conn);
  if (!c || Date.now() - c.at > FRESH) {
    c = { ...c, at: Date.now() };
    cache.set(conn, c);
    load(conn);
  }
  body.insertAdjacentHTML("beforeend", `<div id="host">${draw(conn)}</div>`);
  wire(conn);
  tick();
}

async function load(conn) {
  const c = cache.get(conn);
  const [h, d] = await Promise.allSettled([call("Host", conn), call("HostDevices", conn)]);
  if (h.status === "fulfilled") { c.host = h.value; c.err = ""; } else c.err = errText(h.reason);
  if (d.status === "fulfilled") { c.devs = d.value; c.devErr = ""; } else c.devErr = errText(d.reason);
  redraw(conn);
}

function redraw(conn) {
  const el = $("#host");
  if (!el || shown !== conn) return;
  el.innerHTML = draw(conn);
  wire(conn);
}

const row = (k, v) => `<dt>${esc(k)}</dt><dd>${v}</dd>`;
const yes = (b) => (b ? t("有") : t("没有"));

function draw(conn) {
  const c = cache.get(conn) || {}, h = c.host;
  const about = c.err ? `<p class="msg error">${esc(c.err)}</p>` : !h ? skeleton() : `<dl class="kv">
      ${row(t("主机名"), `<span class="mono">${esc(h.hostname)}</span>`)}
      ${row(t("架构"), esc(h.arch))}
      ${row("CPU", esc(t("{n} 个", { n: h.cpus })))}
      ${row(t("内存"), esc(kib(h.memory_kib)))}
      ${row("libvirt", `<span class="mono">${esc(h.libvirt_version)}</span>`)}
      ${row(t("虚拟化"), `<span class="mono">${esc(h.virt_type)} ${esc(h.hypervisor_version)}</span>`)}
      ${row(t("机器类型"), `<span class="mono">${esc((h.machines || []).join(", "))}</span>`)}
      ${row(t("最多 vCPU"), esc(h.max_vcpus || "–"))}
      ${row("UEFI", esc(yes(h.uefi)) + (h.uefi ? `, ${t("安全启动")}: ${esc(yes(h.secure_boot))}` : ""))}
      ${row("TPM", esc(yes(h.tpm)))}
    </dl>`;
  return `<div class="cards">
      <div class="card"><h3>${t("主机")}</h3>${about}</div>
      <div class="card" id="host-use">${usage(conn)}</div>
    </div>
    <section><h2>${t("主机设备")} <button id="host-reload" class="small">${icon("refresh-cw")}${t("刷新")}</button></h2>${devices(c)}</section>`;
}

function usage(conn) {
  const hs = hostHistory(conn), p = hs.at(-1);
  if (!p) return `<h3>${t("使用情况")}</h3>${skeleton()}`;
  const u = lastUse.get(conn);
  return `<h3>${t("使用情况")}</h3>
    <p class="now-row"><span>CPU <b>${p.cpu == null ? "–" : p.cpu.toFixed(1) + "%"}</b></span>
      <span>${t("内存")} <b>${p.used ? esc(`${kib(p.used)} / ${kib(p.total)}`) : "–"}</b></span>
      ${u ? `<span>${esc(t("运行中 {n} 台, 共 {all} 台", { n: u.running, all: u.machines }))}</span>` : ""}</p>
    <div class="charts two"><div>${chart([{ values: hs.map((x) => x.cpu), cls: "a" }], 100)}<p class="legend"><span class="a">CPU %</span></p></div>
      <div>${chart([{ values: hs.map((x) => x.used || null), cls: "b" }], p.total || undefined)}<p class="legend"><span class="b">${t("内存")}</span></p></div></div>
    ${p.cpu == null && hs.length > 1 ? `<p class="hint">${t("这台主机不报告 CPU 时间。")}</p>` : ""}
    <p class="hint">${esc(t("每 {s} 秒更新; 内存不含内核的缓存。", { s: EVERY / 1000 }))}</p>`;
}

function devices(c) {
  if (c.devErr) return errorCard(t("读不到主机设备"), c.devErr);
  if (!c.devs) return skeleton(true);
  if (!c.devs.length) return emptyState({ small: true, icon: "usb", title: t("没有找到 PCI 或 USB 设备"), text: t("libvirt 在这台主机上没有列出设备。") });
  const name = (d) => [d.vendor || d.vendor_id, d.product || d.product_id].filter(Boolean).join(" ");
  return `<div class="tablewrap"><table class="hostdevs"><thead><tr>
      <th class="opt">${t("类型")}</th><th>${t("总线地址")}</th><th>${t("设备")}</th><th class="opt2">${t("驱动")}</th><th class="opt">${t("IOMMU 组")}</th><th>${t("被谁使用")}</th><th></th>
    </tr></thead><tbody>${c.devs.map((d, i) => `<tr>
      <td class="opt">${esc(d.kind.toUpperCase())}</td>
      <td class="mono">${esc(d.address)}</td>
      <td class="dev">${esc(name(d))}${d.class ? ` <small class="mono">${esc(d.class)}</small>` : ""}</td>
      <td class="mono opt2">${esc(d.driver || "–")}</td>
      <td class="num opt">${d.iommu_group == null ? "–" : esc(d.iommu_group)}</td>
      <td>${d.used_by.length ? esc(d.used_by.join(", ")) : `<span class="mute">–</span>`}</td>
      <td class="ops"><div><button data-i="${i}" aria-label="${esc(t("{name} 的 XML", { name: d.name }))}">${icon("file-code")}XML</button></div></td>
    </tr>`).join("")}</tbody></table></div>
    <p class="hint">${esc(t("把设备交给虚拟机: 在它的「其他设备」里按 XML 添加 <hostdev>; 同一 IOMMU 组的 PCI 设备要一起交出。"))}</p>`;
}

function wire(conn) {
  const el = $("#host");
  if (!el) return;
  $("#host-reload").onclick = () => { cache.set(conn, { at: Date.now() }); redraw(conn); load(conn); };
  el.querySelectorAll("table.hostdevs td.ops button").forEach((b) => {
    b.onclick = () => showXML(conn, cache.get(conn).devs[+b.dataset.i]);
  });
}

async function showXML(conn, d) {
  let x;
  try { x = await call("HostDeviceXML", conn, d.name); } catch (e) { return fail(e); }
  const dlg = $("#dlg-form"), f = $("#form-dialog");
  f.innerHTML = `<h2>${esc(d.name)}</h2><textarea class="xml" rows="18" readonly>${esc(x)}</textarea>
    <div class="buttons"><button type="button" class="primary" id="hx-close">${t("关闭")}</button></div>`;
  $("#hx-close").onclick = () => dlg.close();
  dlg.returnValue = "";
  dlg.showModal();
}

const lastUse = new Map(); // conn -> HostUsage

watch({
  kind: "HostStats",
  conn: () => ($("#host-use") ? shown : null),
  show: (u, err) => {
    const el = $("#host-use");
    if (!el) return;
    if (err) { el.innerHTML = `<h3>${t("使用情况")}</h3><p class="msg error">${esc(t("读不到统计: {err}", { err: errText(err) }))}</p>`; return; }
    lastUse.set(shown, u);
    el.innerHTML = usage(shown);
  },
});
