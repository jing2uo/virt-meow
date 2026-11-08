// The details page's other devices: video, the VNC console and where it
// listens, TPM, sound, RNG, the clipboard, and any device from its XML; what virt-mm device
// does. Graphics stay VNC (ADR-0001): a SPICE console is shown, not offered.
import { call } from "./rpc.js";
import { t } from "./i18n.js";
import { $, esc, fail, formDialog, showEdited, confirmDanger, icon } from "./util.js";
import { addTab, loadMachine, off } from "./machine.js";

const opt = (value, label, sel) => `<option value="${esc(value)}"${value === sel ? " selected" : ""}>${esc(label)}</option>`;
const videoModels = ["virtio", "vga", "qxl", "bochs", "ramfb", "none"];
const soundModels = ["none", "ich9", "ich6", "ich7", "ac97", "usb", "virtio"];

// loopback: an address only the host itself reaches ("" is libvirt's
// default, which virt-meow never writes and is 127.0.0.1 on a stock host).
const loopback = (a) => a === "" || a.startsWith("127.") || a === "::1";

async function edit(fn) {
  try { showEdited(await fn()); } catch (e) { fail(e); }
  loadMachine();
}

addTab({
  id: "devices",
  label: () => t("其他设备"),
  html: (k) => {
    const m = k.info, d = m.devices, running = !off(m.state);
    const video = d.video[0]?.model || "none";
    const sound = d.sound[0] || "none";
    const vnc = d.graphics.find((g) => g.type === "vnc");
    const others = d.graphics.filter((g) => g.type !== "vnc");
    const later = running ? `<p class="hint">${t("运行中: 下次启动时生效。")}</p>` : "";
    return `<div class="cards">
    <form class="card form" id="f-video">
      <h3>${t("显卡")}</h3>
      <label><span>${t("型号")}</span><select name="model">${videoModels.map((x) => opt(x, x === "none" ? t("无 (没有显示)") : x, video)).join("")}
        ${videoModels.includes(video) ? "" : opt(video, video, video)}</select></label>
      <p class="hint">${t("virtio 适合大多数系统; 没有 virtio 驱动的系统用 vga 或 bochs。")}</p>
      ${later}
      <div class="buttons"><button type="submit" class="primary">${t("应用")}</button></div>
    </form>
    <form class="card form" id="f-vnc">
      <h3>${t("VNC 控制台")}</h3>
      ${vnc ? `<label><span>${t("监听地址")}</span><input name="listen" value="${esc(vnc.listen)}" spellcheck="false" placeholder="127.0.0.1"></label>
        ${!loopback(vnc.listen) ? `<p class="warn">${esc(t("它在 {addr} 上监听: 网络上的任何人都能不加密、不用密码地打开它的控制台。", { addr: vnc.listen }))}</p>` : ""}
        <p class="hint">${t("127.0.0.1 只让主机自己连; virt-meow 在主机上连它 (远程主机经过 SSH), 不需要别的地址。")}</p>
        ${vnc.port ? `<p class="hint">${esc(t("现在的端口: {port}", { port: vnc.port }))}</p>` : ""}`
      : `<p class="mute">${t("没有 VNC 控制台")}</p>`}
      ${others.map((g) => `<p class="hint">${esc(t("还有一个 {type} 控制台 ({listen}); virt-meow 只管理 VNC。", { type: g.type.toUpperCase(), listen: g.listen || t("默认") }))}</p>`).join("")}
      ${later}
      ${vnc ? `<div class="pw-row"><span>${t("密码")}: ${vnc.password ? t("已设置") : t("无")}</span>
          <span>${vnc.password ? `<button type="button" class="small" id="vnc-pw-rm">${t("清除")}</button>` : ""}<button type="button" class="small" id="vnc-pw">${vnc.password ? t("更改…") : t("设置…")}</button></span></div>` : ""}
      <div class="buttons">${vnc ? `<button type="button" class="del" id="vnc-rm">${t("移除")}</button><button type="submit" class="primary">${t("应用")}</button>`
        : `<button type="button" class="primary" id="vnc-add">${t("添加 VNC 控制台")}</button>`}</div>
    </form>
    <form class="card form" id="f-sound">
      <h3>${t("声卡")}</h3>
      <label><span>${t("型号")}</span><select name="model">${soundModels.map((x) => opt(x, x === "none" ? t("无") : x, sound)).join("")}
        ${soundModels.includes(sound) ? "" : opt(sound, sound, sound)}</select></label>
      <p class="hint">${t("ich9 适合 q35 机器, ich6 适合较旧的 i440fx。")}</p>
      ${later}
      <div class="buttons"><button type="submit" class="primary">${t("应用")}</button></div>
    </form>
    <div class="card form">
      <h3>TPM</h3>
      ${d.tpm.length ? `<p>${esc(d.tpm.map((x) => `${x.model}, ${x.backend}${x.version ? " " + x.version : ""}`).join("; "))}</p>`
        : `<p class="mute">${t("没有 TPM")}</p>`}
      <p class="hint">${t("Windows 11 需要 TPM 2.0。模拟的 TPM 由主机上的 swtpm 提供, 它的状态在删除虚拟机时一起删除。")}</p>
      ${later}
      <div class="buttons">${d.tpm.length ? `<button type="button" class="del" id="tpm-rm">${t("移除 TPM")}</button>` : `<button type="button" class="primary" id="tpm-add">${t("添加 TPM 2.0")}</button>`}</div>
    </div>
    <div class="card form">
      <h3>${t("随机数生成器")}</h3>
      ${d.rng.length ? `<p>${esc(d.rng.map((x) => `${x.model}, ${x.backend}`).join("; "))}</p>` : `<p class="mute">${t("没有随机数生成器")}</p>`}
      <p class="hint">${t("virtio RNG 从主机的 /dev/urandom 取熵, 让客户机启动时不必等熵。运行中立即生效。")}</p>
      <div class="buttons">${d.rng.length ? `<button type="button" class="del" id="rng-rm">${t("移除")}</button>` : `<button type="button" class="primary" id="rng-add">${t("添加 virtio RNG")}</button>`}</div>
    </div>
    <div class="card form">
      <h3>${t("剪贴板")}</h3>
      ${d.clipboard ? `<p>${t("与 VNC 控制台共享")}</p>` : `<p class="mute">${t("不共享")}</p>`}
      <p class="hint">${t("在控制台和这台电脑之间复制粘贴 (QEMU 的 qemu-vdagent)。客户机里需要 spice-vdagent; 没有它时用控制台「按键」里的「输入文本…」。运行中立即生效。主机的 QEMU 早于 10.1 时, 共享期间运行中的虚拟机不能保存状态、做带内存的快照或迁移。")}</p>
      <div class="buttons">${d.clipboard ? `<button type="button" class="del" id="clip-rm">${t("停止共享")}</button>` : `<button type="button" class="primary" id="clip-add">${t("共享剪贴板")}</button>`}</div>
    </div>
    <div class="card form">
      <h3>${t("主机设备 (USB、PCI)")}</h3>
      ${d.hostdevs.length ? `<ul class="plain">${d.hostdevs.map((h, i) => `<li><span class="mono">${esc(h.kind.toUpperCase())} ${esc(h.ref)}</span>
        <button type="button" class="del small" data-hd="${i}">${icon("trash-2")}${t("移除")}</button></li>`).join("")}</ul>` : `<p class="mute">${t("没有主机设备")}</p>`}
      <p class="hint">${t("把主机的 USB 或 PCI 设备交给虚拟机: 它运行时设备从主机上拿走, 关机后还回去。")}</p>
      ${later}
      <div class="buttons"><button type="button" class="primary" id="hd-add">${icon("usb")}${t("添加主机设备…")}</button></div>
    </div>
    <form class="card form" id="f-fs">
      <h3>${t("共享目录 (virtiofs)")}</h3>
      ${d.filesystems.length ? `<ul class="plain">${d.filesystems.map((f, i) => `<li><span><span class="mono">${esc(f.source)}</span> → <span class="mono">${esc(f.tag)}</span>${f.driver !== "virtiofs" ? ` <small>${esc(f.driver)}</small>` : ""}</span>
        <button type="button" class="del small" data-fs="${i}">${icon("trash-2")}${t("移除")}</button></li>`).join("")}</ul>` : `<p class="mute">${t("没有共享目录")}</p>`}
      <div class="grid">
        <label><span>${t("主机上的目录")}</span><input name="dir" placeholder="/srv/share" spellcheck="false" required></label>
        <label><span>${t("标签")}</span><input name="tag" placeholder="share" maxlength="36" spellcheck="false" required></label>
      </div>
      <p class="hint">${t("下次启动时生效。主机需要 virtiofsd, 目录要在主机上存在; 需要时会把虚拟机内存设为共享 (memfd)。客户机里: mount -t virtiofs <标签> /mnt")}</p>
      <div class="buttons"><button type="submit" class="primary">${icon("folder-open")}${t("添加共享目录")}</button></div>
    </form>
    <div class="card form">
      <h3>${t("其他设备 (XML)")}</h3>
      <p class="hint">${esc(t("看门狗、串口、额外的输入设备等等: 写出设备的 XML (<devices> 里的一个元素), 和 virsh attach-device 一样。"))}</p>
      <div class="buttons"><button type="button" id="xml-detach">${t("按 XML 移除…")}</button><button type="button" class="primary" id="xml-attach">${t("按 XML 添加…")}</button></div>
    </div></div>`;
  },
  wire: (k, body) => {
    const m = k.info, d = m.devices;
    const c = (method, ...args) => edit(() => call(method, k.conn, k.uuid, ...args));
    $("#f-video", body).onsubmit = (e) => {
      e.preventDefault();
      const v = e.target.elements.model.value;
      if (v !== (d.video[0]?.model || "none")) c("SetVideo", v);
    };
    $("#f-sound", body).onsubmit = (e) => {
      e.preventDefault();
      const v = e.target.elements.model.value;
      if (v !== (d.sound[0] || "none")) c("SetSound", v);
    };
    const vnc = d.graphics.find((g) => g.type === "vnc");
    $("#f-vnc", body).onsubmit = async (e) => {
      e.preventDefault();
      const addr = e.target.elements.listen.value.trim() || "127.0.0.1";
      if (addr === vnc.listen) return;
      if (!(await askListen(addr))) return;
      c("SetGraphicsListen", "vnc", addr);
    };
    if ($("#vnc-pw", body)) $("#vnc-pw", body).onclick = () => vncPassword(k);
    if ($("#vnc-pw-rm", body)) $("#vnc-pw-rm", body).onclick = async () => {
      if (await confirmDanger(t("清除 VNC 密码"), t("清除 {name} 的 VNC 密码? 从下次启动起, 能连到它端口的人不用密码就能看到并操作它的屏幕。", { name: k.name }), t("清除"))) c("SetGraphicsPassword", "vnc", "");
    };
    if ($("#vnc-add", body)) $("#vnc-add", body).onclick = () => c("AddGraphics", "vnc", "");
    if ($("#vnc-rm", body)) $("#vnc-rm", body).onclick = async () => {
      if (await confirmDanger(t("移除 VNC 控制台"), t("移除 {name} 的 VNC 控制台? 之后在 virt-meow 里看不到它的屏幕。", { name: k.name }), t("移除"))) c("RemoveGraphics", "vnc");
    };
    if ($("#tpm-add", body)) $("#tpm-add", body).onclick = () => c("AddTPM", "");
    if ($("#tpm-rm", body)) $("#tpm-rm", body).onclick = async () => {
      if (await confirmDanger(t("移除 TPM"), t("移除 {name} 的 TPM? 用 TPM 保存的密钥 (BitLocker 等) 在客户机里将无法使用。", { name: k.name }), t("移除"))) c("RemoveTPM");
    };
    if ($("#clip-add", body)) $("#clip-add", body).onclick = () => c("AddClipboard");
    if ($("#clip-rm", body)) $("#clip-rm", body).onclick = () => c("RemoveClipboard");
    if ($("#rng-add", body)) $("#rng-add", body).onclick = () => c("AddRNG");
    if ($("#rng-rm", body)) $("#rng-rm", body).onclick = () => c("RemoveRNG");
    $("#hd-add", body).onclick = () => addHostDevice(k);
    $("#f-fs", body).onsubmit = (e) => {
      e.preventDefault();
      const f = e.target.elements;
      c("AddFilesystem", f.dir.value.trim(), f.tag.value.trim());
    };
    body.querySelectorAll("[data-fs]").forEach((b) => {
      const f = d.filesystems[+b.dataset.fs];
      b.onclick = async () => {
        if (await confirmDanger(t("移除共享目录"), t("不再把 {dir} 共享给 {name} (下次启动时生效)?", { dir: f.source, name: k.name }), t("移除"))) c("RemoveFilesystem", f.tag);
      };
    });
    body.querySelectorAll("[data-hd]").forEach((b) => {
      const h = d.hostdevs[+b.dataset.hd];
      b.onclick = async () => {
        if (await confirmDanger(t("移除主机设备"), t("从 {name} 移除主机设备 {dev}?", { name: k.name, dev: `${h.kind.toUpperCase()} ${h.ref}` }), t("移除"))) c("DetachHostDevice", h.ref);
      };
    });
    $("#xml-attach", body).onclick = () => deviceXML(k, true);
    $("#xml-detach", body).onclick = () => deviceXML(k, false);
  },
});

// vncPassword sets the password the VNC console asks for, saying what it
// doesn't do: encrypt.
function vncPassword(k) {
  formDialog({
    title: t("{name} 的 VNC 密码", { name: k.name }),
    body: `<label><span>${t("新密码")}</span><input type="password" name="pw" maxlength="8" autocomplete="new-password" required></label>
      <label><span>${t("再输一次")}</span><input type="password" name="again" maxlength="8" autocomplete="new-password" required></label>
      <p class="warn">${t("密码不会加密 VNC: 能连到端口的人仍能看到经过的画面和按键, VNC 自己的认证也很弱。让它只在 127.0.0.1 上监听, virt-meow 经 SSH 连它。")}</p>
      <p class="hint">${t("最多 8 个字符 (VNC 只用前 8 个)。控制台连接时会问它。运行中的虚拟机立即生效, 除非它启动时控制台没有密码 (那要等下次启动); 清除密码总要等下次启动。")}</p>`,
    ok: t("设置密码"),
    submit: async (f) => {
      if (f.pw.value !== f.again.value) throw new Error(t("两次输入的密码不一样"));
      showEdited(await call("SetGraphicsPassword", k.conn, k.uuid, "vnc", f.pw.value));
      loadMachine();
    },
  });
}

// askListen asks before a console is put where the network reaches it.
function askListen(addr) {
  if (loopback(addr)) return Promise.resolve(true);
  return confirmDanger(t("在网络上打开控制台"), t("让 VNC 在 {addr} 上监听? 能连到这台主机这个地址的任何人都能看到并操作这台虚拟机的屏幕: VNC 不加密, 也没有密码。virt-meow 自己不需要这样做, 它在主机上连接 127.0.0.1 (远程主机经过 SSH)。", { addr }), t("仍然监听"));
}

// addHostDevice lists the host's USB and PCI devices (virt-mm hostdevs),
// with who has each and its IOMMU group, and gives the machine the one
// picked.
async function addHostDevice(k) {
  let ds;
  try { ds = await call("HostDevices", k.conn); } catch (e) { return fail(e); }
  const mine = (x) => x.used_by.includes(k.name);
  const group = (x) => x.iommu_group == null ? [] : ds.filter((o) => o !== x && o.iommu_group === x.iommu_group);
  const label = (x) => [x.vendor, x.product].filter(Boolean).join(" ");
  const rows = ds.map((x, i) => {
    const g = group(x);
    return `<tr><td><input type="radio" name="dev" value="${i}"${mine(x) ? " disabled" : ""}></td>
      <td class="opt">${esc(x.kind.toUpperCase())}</td><td class="mono">${esc(x.address)}</td>
      <td class="wrap">${esc(label(x))}${label(x) ? " " : ""}<span class="mono">${esc(x.vendor_id + ":" + x.product_id)}</span></td>
      <td class="mono opt2">${esc(x.driver || "–")}</td>
      <td>${x.iommu_group == null ? "–" : esc(x.iommu_group)}${g.length ? ` <small class="warn">${esc(t("组内还有 {n} 个", { n: g.length }))}</small>` : ""}</td>
      <td>${mine(x) ? t("已在这台") : x.used_by.length ? `<span class="warn">${esc(x.used_by.join(", "))}</span>` : "–"}</td></tr>`;
  }).join("");
  formDialog({
    title: t("给 {name} 添加主机设备", { name: k.name }),
    body: ds.length ? `<div class="tablewrap"><table class="pick"><thead><tr><th></th><th class="opt">${t("类型")}</th><th>${t("总线地址")}</th><th>${t("设备")}</th>
        <th class="opt2">${t("驱动")}</th><th>${t("IOMMU 组")}</th><th>${t("被谁使用")}</th></tr></thead><tbody>${rows}</tbody></table></div>
      <p class="hint">${t("PCI 设备连同它 IOMMU 组里的其他设备一起离开主机; 同一个设备同时只能给一台运行中的虚拟机。没有名字的设备, 主机缺少 udev 的硬件数据库。")}</p>`
      : `<p class="mute">${t("主机上没有列出 USB 或 PCI 设备。")}</p>`,
    ok: t("添加"),
    submit: async (f) => {
      const r = f.dev;
      const v = r instanceof RadioNodeList ? r.value : r && r.checked ? r.value : "";
      if (v === "") throw new Error(t("先选一个设备"));
      showEdited(await call("AttachHostDevice", k.conn, k.uuid, ds[+v].name));
      loadMachine();
    },
  });
}

function deviceXML(k, attach) {
  formDialog({
    title: attach ? t("给 {name} 按 XML 添加设备", { name: k.name }) : t("从 {name} 按 XML 移除设备", { name: k.name }),
    body: `<label><span>${t("设备的 XML")}</span><textarea name="xml" rows="9" class="mono" spellcheck="false" placeholder="${esc("<input type='tablet' bus='usb'/>")}"></textarea></label>
      <p class="hint">${attach ? t("写进定义; 运行中的虚拟机能接上的立即接上, 结果会说明。")
        : t("写出要移除的设备, 能认出它就行 (比如磁盘的 target, 网卡的 MAC); 可以从虚拟机的 XML 里复制。")}</p>`,
    ok: attach ? t("添加") : t("移除"),
    danger: !attach,
    submit: async (f) => {
      showEdited(await call(attach ? "AttachDevice" : "DetachDevice", k.conn, k.uuid, f.xml.value));
      loadMachine();
    },
  });
}
