// The details page's network interfaces: what virt-mm nic does. The
// networks and bridges to pick from are CreateOptions'.
import { call } from "./rpc.js";
import { t } from "./i18n.js";
import { $, esc, fail, formDialog, showEdited, confirmDanger, icon, emptyState } from "./util.js";
import { addTab, loadMachine, off, nicSource, nicAddrs, addrHint } from "./machine.js";

const opt = (value, label, sel, dis = false) => `<option value="${esc(value)}"${value === sel ? " selected" : ""}${dis ? " disabled" : ""}>${esc(label)}</option>`;
const models = ["virtio", "e1000e", "e1000", "rtl8139"];

async function edit(fn) {
  try { showEdited(await fn()); } catch (e) { fail(e); }
  loadMachine();
}

addTab({
  id: "nics",
  label: () => t("网卡"),
  html: (k) => {
    const m = k.info, ns = m.devices.nics;
    return `<section>
      <div class="toolbar"><button id="n-add">${icon("plus")}${t("添加网卡…")}</button></div>
      ${ns.length === 0 ? emptyState({ small: true, icon: "network", title: t("没有网卡"), text: t("添加一块网卡, 接到虚拟网络或主机的网桥上。") }) : `<div class="tablewrap"><table><thead><tr>
        <th>MAC</th><th>${t("连接到")}</th><th class="opt2">${t("型号")}</th><th class="opt">${t("链路")}</th><th>${t("地址")}</th><th></th>
      </tr></thead><tbody>${ns.map((n, i) => `<tr>
        <td class="mono">${esc(n.mac)}</td>
        <td class="wrap">${esc(nicSource(n))}</td>
        <td class="opt2">${esc(n.model || "–")}</td>
        <td class="opt">${n.link === "down" ? `<span class="warn">${t("断开")}</span>` : t("连通")}</td>
        <td class="mono wrap">${esc(nicAddrs(m, n.mac).join(", ")) || `<span class="mute">–</span>`}</td>
        <td class="ops"><div><button data-i="${i}" data-a="link">${icon(n.link === "down" ? "cable" : "unplug")}${n.link === "down" ? t("接通") : t("断开链路")}</button><button class="del" data-i="${i}" data-a="remove">${icon("trash-2")}${t("移除")}</button></div></td>
      </tr>`).join("")}</tbody></table></div>
      <p class="hint">${addrHint(m)}</p>`}
      <p class="hint">${t("断开链路相当于拔掉网线, 网卡还在; 运行中的虚拟机立即生效。")}</p>
    </section>`;
  },
  wire: (k, body) => {
    const ns = k.info.devices.nics;
    $("#n-add", body).onclick = () => addNIC(k);
    body.querySelectorAll("td.ops button").forEach((b) => {
      const n = ns[+b.dataset.i];
      b.onclick = () => b.dataset.a === "link" ? edit(() => call("SetLink", k.conn, k.uuid, n.mac, n.link === "down")) : remove(k, n);
    });
  },
});

async function addNIC(k) {
  let o;
  try { o = await call("CreateOptions", k.conn); } catch (e) { return fail(e); }
  const nets = o.networks, brs = o.bridges;
  const first = k.info.devices.nics[0];
  formDialog({
    title: t("给 {name} 添加网卡", { name: k.name }),
    body: `<div class="radio">
        <label><input type="radio" name="kind" value="network"${nets.length ? " checked" : " disabled"}>${t("虚拟网络")}</label>
        <label><input type="radio" name="kind" value="bridge"${!nets.length && brs.length ? " checked" : ""}${brs.length ? "" : " disabled"}>${t("网桥")}</label>
        <label><input type="radio" name="kind" value="user"${nets.length || brs.length ? "" : " checked"}>${t("用户态网络 (NAT)")}</label></div>
      <label data-kind="network"><span>${t("虚拟网络")}</span><select name="network">${nets.map((n) => opt(n.name, n.active ? n.name : t("{name} (未启动)", { name: n.name }), nets.find((x) => x.active)?.name)).join("")}</select></label>
      <label data-kind="bridge"><span>${t("主机上的网桥")}</span><select name="bridge">${brs.map((b) => opt(b, b, brs[0])).join("")}</select></label>
      <p class="hint" data-kind="user">${t("用户态网络不需要主机配置, 但客户机只能向外连, 外面连不进来。")}</p>
      <div class="row">
        <label><span>${t("型号")}</span><select name="model">${opt("", first ? t("与第一块网卡相同 ({model})", { model: first.model }) : "virtio", "")}${models.map((x) => opt(x, x, "")).join("")}</select></label>
        <label><span>${t("MAC 地址 (可选)")}</span><input name="mac" placeholder="52:54:00:…" spellcheck="false"></label>
      </div>
      <p class="hint">${off(k.info.state) ? t("virtio 性能最好; 没有 virtio 驱动的系统 (较旧的 Windows) 用 e1000e。") : t("运行中: 机器能热插拔时立即接上, 否则在下次启动时生效。")}</p>`,
    ok: t("添加"),
    submit: async (f) => {
      const kind = f.kind.value, n = { kind, model: f.model.value, mac: f.mac.value.trim() };
      if (kind === "network") n.source = f.network.value;
      if (kind === "bridge") n.source = f.bridge.value;
      showEdited(await call("AddNIC", k.conn, k.uuid, n));
      loadMachine();
    },
  });
  const f = $("#form-dialog");
  const show = () => f.querySelectorAll("[data-kind]").forEach((el) => { el.hidden = el.dataset.kind !== f.elements.kind.value; });
  f.querySelectorAll("[name=kind]").forEach((r) => { r.onchange = show; });
  show();
}

async function remove(k, n) {
  if (!(await confirmDanger(t("移除网卡"), t("从 {name} 移除网卡 {mac} ({source})? 客户机失去这块网卡和它的 MAC 地址, 给它的地址配置跟着失效。", { name: k.name, mac: n.mac, source: nicSource(n) }), t("移除")))) return;
  edit(() => call("RemoveNIC", k.conn, k.uuid, n.mac));
}
