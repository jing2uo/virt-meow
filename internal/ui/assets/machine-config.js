// The details page's CPU and memory, and its firmware and boot order: what
// virt-mm set and virt-mm boot change.
import { call } from "./rpc.js";
import { t } from "./i18n.js";
import { $, esc, errText, kib, showEdited } from "./util.js";
import { addTab, loadMachine, trackDirty, off } from "./machine.js";

const opt = (value, label, sel, dis, title = "") => `<option value="${esc(value)}"${value === sel ? " selected" : ""}${dis ? " disabled" : ""}${title ? ` title="${esc(title)}"` : ""}>${esc(label)}</option>`;

// apply runs an edit from a form: its error under the form, its outcome as
// a toast, and the machine read again.
async function apply(form, fn) {
  const err = form.querySelector(".error"), btn = form.querySelector("[type=submit]");
  err.textContent = "";
  btn.disabled = true;
  try {
    const e = await fn();
    if (e) showEdited(e);
    form.classList.remove("dirty");
    loadMachine();
  } catch (x) {
    err.textContent = errText(x);
  }
  btn.disabled = false;
}

// ---- CPU and memory ---------------------------------------------------------

addTab({
  id: "cpu",
  label: () => t("CPU 与内存"),
  html: (k) => {
    const m = k.info, c = m.config, running = !off(m.state);
    const topo = c.sockets > 0;
    const mib = (n) => Math.round(n / 1024);
    return `<div class="cards">
    <form class="card form" id="f-cpu">
      <h3>CPU</h3>
      ${running && m.vcpus !== c.vcpus ? `<p class="hint">${esc(t("现在有 {now} 个 vCPU, 下次启动时 {next} 个。", { now: m.vcpus, next: c.vcpus }))}</p>` : ""}
      <div class="grid">
        <label><span>${t("vCPU 数")}</span><input name="vcpus" type="number" min="1" value="${c.vcpus}"${topo ? " readonly" : ""}></label>
        <label><span>${t("CPU 型号")}</span><select name="model"><option>${esc(m.cpu || t("读取中…"))}</option></select></label>
      </div>
      <label class="check"><input type="checkbox" name="topo"${topo ? " checked disabled" : ""}><span>${t("设置拓扑 (插槽、核心、线程)")}</span></label>
      <div class="grid three" id="topo"${topo ? "" : " hidden"}>
        <label><span>${t("插槽")}</span><input name="sockets" type="number" min="1" value="${c.sockets || 1}"></label>
        <label><span>${t("每插槽核心")}</span><input name="cores" type="number" min="1" value="${c.cores || c.vcpus}"></label>
        <label><span>${t("每核心线程")}</span><input name="threads" type="number" min="1" value="${c.threads || 1}"></label>
      </div>
      <p class="hint" id="cpu-models-note"></p>
      ${running ? `<p class="hint">${t("运行中: vCPU 数在最大值以内立即生效; 拓扑和型号在下次启动时生效。")}</p>` : ""}
      <p class="error"></p>
      <div class="buttons"><button type="submit" class="primary">${t("应用")}</button></div>
    </form>
    <form class="card form" id="f-mem">
      <h3>${t("内存")}</h3>
      ${running && (m.memory_kib !== c.memory_kib || m.max_memory_kib !== c.max_memory_kib) ? `<p class="hint">${esc(t("现在 {now}, 下次启动时 {next}。", { now: kib(m.memory_kib), next: kib(c.memory_kib) }))}</p>` : ""}
      <div class="grid">
        <label><span>${t("内存 (MiB)")}</span><input name="mem" type="number" min="64" step="64" value="${mib(c.memory_kib)}"></label>
        <label><span>${t("最大内存 (MiB)")}</span><input name="max" type="number" min="64" step="64" value="${mib(c.max_memory_kib)}"></label>
      </div>
      <p class="hint">${t("最大内存是客户机能通过气球驱动增长到的上限, 改动在下次启动时生效。")}
        ${running ? t("运行中: 内存在最大值以内立即生效 (需要客户机的气球驱动)。") : ""}</p>
      <p class="error"></p>
      <div class="buttons"><button type="submit" class="primary">${t("应用")}</button></div>
    </form></div>`;
  },
  wire: (k, body) => {
    const m = k.info, c = m.config;
    const fc = $("#f-cpu", body), fm = $("#f-mem", body), f = fc.elements;
    trackDirty(fc);
    trackDirty(fm);
    const product = () => +f.sockets.value * +f.cores.value * +f.threads.value;
    const syncTopo = () => {
      $("#topo", body).hidden = !f.topo.checked;
      f.vcpus.readOnly = f.topo.checked;
      if (f.topo.checked) f.vcpus.value = product();
    };
    f.topo.onchange = () => {
      if (f.topo.checked) { f.sockets.value = 1; f.cores.value = f.vcpus.value; f.threads.value = 1; }
      syncTopo();
    };
    for (const n of ["sockets", "cores", "threads"]) f[n].oninput = syncTopo;
    loadModels(k, fc);
    fc.onsubmit = (e) => {
      e.preventDefault();
      const s = {};
      if (f.topo.checked) {
        const tp = { sockets: +f.sockets.value, cores: +f.cores.value, threads: +f.threads.value };
        if (tp.sockets !== c.sockets || tp.cores !== c.cores || tp.threads !== c.threads) Object.assign(s, tp, { vcpus: product() });
      } else if (+f.vcpus.value !== c.vcpus) s.vcpus = +f.vcpus.value;
      if (f.model.value && f.model.value !== m.cpu && !f.model.disabled) s.cpu_model = f.model.value;
      if (Object.keys(s).length === 0) return fc.classList.remove("dirty");
      apply(fc, () => call("Set", k.conn, k.uuid, s));
    };
    fm.onsubmit = (e) => {
      e.preventDefault();
      const s = {}, mem = +fm.elements.mem.value, max = +fm.elements.max.value;
      if (max !== Math.round(c.max_memory_kib / 1024)) s.max_memory_mib = max;
      if (mem !== Math.round(c.memory_kib / 1024)) s.memory_mib = mem;
      if (Object.keys(s).length === 0) return fm.classList.remove("dirty");
      apply(fm, () => call("Set", k.conn, k.uuid, s));
    };
  },
});

// The CPU models come from the host's domain capabilities: one call when
// the tab opens, kept for the machine.
async function loadModels(k, form) {
  const sel = form.elements.model;
  if (!k.cpuModels) {
    sel.disabled = true;
    try {
      k.cpuModels = await call("CPUModels", k.conn, k.uuid);
    } catch (e) {
      $("#cpu-models-note").textContent = t("读不到这台主机的 CPU 型号: {err}", { err: errText(e) });
      return;
    }
  }
  if (!form.isConnected) return;
  const cm = k.cpuModels, cur = k.info.cpu;
  const names = new Set([...cm.modes, ...cm.models.map((x) => x.name)]);
  const usable = cm.models.filter((x) => x.usable !== "no"), not = cm.models.filter((x) => x.usable === "no");
  sel.innerHTML = (names.has(cur) || !cur ? "" : opt(cur, cur, cur))
    + (cm.modes.length ? `<optgroup label="${esc(t("模式"))}">${cm.modes.map((x) => opt(x, x, cur)).join("")}</optgroup>` : "")
    + `<optgroup label="${esc(t("具体型号"))}">${usable.map((x) => opt(x.name, x.name + (x.vendor && x.vendor !== "unknown" ? ` (${x.vendor})` : ""), cur)).join("")}</optgroup>`
    + (not.length ? `<optgroup label="${esc(t("这台主机不支持"))}">${not.map((x) => opt(x.name, x.name, cur, true, t("缺少: {f}", { f: (x.blockers || []).join(", ") }))).join("")}</optgroup>` : "");
  sel.disabled = false;
  if (!cm.modes.includes("host-passthrough")) $("#cpu-models-note").textContent = t("host-passthrough 需要 KVM, 这台虚拟机不用 KVM 运行。");
}

// ---- firmware and boot ------------------------------------------------------

// bootDevices lists what a machine can boot from in its current order:
// the devices it boots from first, by their order, then the others. Under
// the older per-type order (<boot dev='hd'/>) the types stand for their
// devices.
function bootDevices(d) {
  const all = [
    ...d.disks.map((x) => ({ key: x.target, kind: x.device, label: `${t(x.device)} ${x.target}`, detail: x.source || t("空"), boot: x.boot })),
    ...d.nics.map((x) => ({ key: x.mac, kind: "network", label: t("网卡 {mac}", { mac: x.mac }), detail: x.source || x.kind, boot: x.boot })),
  ];
  if (d.boot_devs && d.boot_devs.length) {
    const kind = { hd: ["disk"], cdrom: ["cdrom"], network: ["network"], fd: ["floppy"] };
    let n = 0;
    for (const dev of d.boot_devs) for (const x of all) if ((kind[dev] || []).includes(x.kind) && !x.boot) x.boot = ++n;
  }
  return [...all.filter((x) => x.boot).sort((a, b) => a.boot - b.boot), ...all.filter((x) => !x.boot)];
}

addTab({
  id: "boot",
  label: () => t("固件与启动"),
  html: (k) => {
    const m = k.info, running = !off(m.state), d = m.devices;
    return `<div class="cards">
    <form class="card form" id="f-fw">
      <h3>${t("固件")}</h3>
      <label><span>${t("固件")}</span><select name="fw"${running ? " disabled" : ""}>
        ${opt("bios", "BIOS", m.firmware)}${opt("uefi", "UEFI", m.firmware)}${opt("uefi-secure", t("UEFI + 安全启动"), m.firmware)}
        ${["bios", "uefi", "uefi-secure"].includes(m.firmware) ? "" : opt(m.firmware, m.firmware, m.firmware)}</select></label>
      <p class="hint">${running ? t("关机后才能改固件。") : t("换固件后, libvirt 重新挑选固件文件; 旧的 NVRAM 不再使用。安全启动需要 q35 机器类型。")}</p>
      <p class="error"></p>
      <div class="buttons"><button type="submit" class="primary"${running ? " disabled" : ""}>${t("应用")}</button></div>
    </form>
    <form class="card form" id="f-boot">
      <h3>${t("启动顺序")}</h3>
      ${d.boot_devs && d.boot_devs.length ? `<p class="hint">${esc(t("它用的是按类型的启动顺序 ({devs}); 保存后改为按设备的顺序。", { devs: d.boot_devs.join(", ") }))}</p>` : ""}
      <ol class="boot" id="boot-list"></ol>
      <label class="check"><input type="checkbox" name="menu"${d.boot_menu ? " checked" : ""}><span>${t("启动时显示启动菜单")}</span></label>
      <p class="hint">${running ? t("运行中: 下次启动时生效。") : t("勾选的设备按顺序启动, 没勾选的不用来启动。")}</p>
      <p class="error"></p>
      <div class="buttons"><button type="submit" class="primary">${t("应用")}</button></div>
    </form></div>`;
  },
  wire: (k, body) => {
    const m = k.info;
    const ff = $("#f-fw", body), fb = $("#f-boot", body);
    trackDirty(ff);
    trackDirty(fb);
    ff.onsubmit = (e) => {
      e.preventDefault();
      const fw = ff.elements.fw.value;
      if (fw === m.firmware) return ff.classList.remove("dirty");
      apply(ff, () => call("Set", k.conn, k.uuid, { firmware: fw }));
    };
    const list = bootDevices(m.devices).map((x) => ({ ...x, on: !!x.boot }));
    const before = list.filter((x) => x.on).map((x) => x.key).join(" ");
    const draw = () => {
      $("#boot-list", body).innerHTML = list.length === 0 ? `<li class="mute">${t("没有磁盘或网卡可以启动")}</li>` : list.map((x, i) => `<li>
        <label class="check"><input type="checkbox" data-i="${i}"${x.on ? " checked" : ""}><span>${esc(x.label)} <small class="mono">${esc(x.detail)}</small></span></label>
        <span class="move"><button type="button" data-up="${i}" ${i === 0 ? "disabled" : ""} aria-label="${esc(t("上移"))}" title="${esc(t("上移"))}">↑</button><button type="button" data-down="${i}" ${i === list.length - 1 ? "disabled" : ""} aria-label="${esc(t("下移"))}" title="${esc(t("下移"))}">↓</button></span>
      </li>`).join("");
    };
    draw();
    $("#boot-list", body).onclick = (e) => {
      const b = e.target.closest("button");
      if (!b) return;
      const i = +(b.dataset.up ?? b.dataset.down), j = b.dataset.up !== undefined ? i - 1 : i + 1;
      [list[i], list[j]] = [list[j], list[i]];
      fb.classList.add("dirty");
      draw();
    };
    $("#boot-list", body).onchange = (e) => {
      const i = e.target.dataset.i;
      if (i !== undefined) list[+i].on = e.target.checked;
    };
    fb.onsubmit = (e) => {
      e.preventDefault();
      const devs = list.filter((x) => x.on).map((x) => x.key);
      const menu = fb.elements.menu.checked;
      if (devs.length === 0) { fb.querySelector(".error").textContent = t("至少勾选一个启动设备。"); return; }
      const changed = devs.join(" ") !== before || (m.devices.boot_devs || []).length > 0;
      if (!changed && menu === m.devices.boot_menu) return fb.classList.remove("dirty");
      apply(fb, () => call("SetBoot", k.conn, k.uuid, changed ? devs : null, menu === m.devices.boot_menu ? null : menu));
    };
  },
});
