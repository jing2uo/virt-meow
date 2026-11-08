// The details page's snapshots: what virt-mm snap does. The tree comes in
// one call each time the tab is drawn, which every change to the machine
// does.
import { call } from "./rpc.js";
import { t } from "./i18n.js";
import { $, esc, fail, errText, formDialog, showEdited, confirmDanger, icon, errorCard, emptyState, skeleton } from "./util.js";
import { addTab, loadMachine, off, opKey } from "./machine.js";
import { runOp, busy } from "./ops.js";

addTab({
  id: "snapshots",
  label: () => t("快照"),
  html: (k) => `<section>
      <div class="toolbar"><button id="s-create"${busy(opKey(k.conn, k.uuid)) ? " disabled" : ""}>${icon("camera")}${t("创建快照…")}</button></div>
      <div id="s-list">${skeleton(true)}</div>
      <p class="hint">${off(k.info.state) ? t("关机时的快照只含磁盘; 运行时的快照还含内存, 恢复后从那一刻继续运行。")
        : t("运行中: 快照含内存, 保存期间虚拟机暂停。")}
        ${t("快照保存在 qcow2 磁盘内部; raw 磁盘的虚拟机不能拍快照。")}</p>
    </section>`,
  wire: (k, body) => {
    $("#s-create", body).onclick = () => create(k);
    load(k, body);
  },
});

async function load(k, body) {
  let ss;
  try { ss = await call("Snapshots", k.conn, k.uuid); } catch (e) {
    if (body.isConnected) $("#s-list", body).innerHTML = errorCard(t("读不到快照"), errText(e));
    return;
  }
  if (!body.isConnected) return;
  const el = $("#s-list", body);
  if (ss.length === 0) {
    el.innerHTML = emptyState({ small: true, icon: "camera", title: t("还没有快照"), text: t("快照记下磁盘 (运行时还有内存) 此刻的样子, 以后可以回到这里。") });
    return;
  }
  const op = busy(opKey(k.conn, k.uuid));
  el.innerHTML = `<div class="tablewrap"><table><thead><tr>
      <th>${t("名称")}</th><th class="opt2">${t("创建时间")}</th><th>${t("当时状态")}</th><th class="opt">${t("描述")}</th><th></th>
    </tr></thead><tbody>${ss.map((s, i) => `<tr>
      <td class="mono"><span class="tree" style="padding-left:${s.depth * 18}px">${s.depth ? "└ " : ""}</span>${esc(s.name)}${s.current ? ` <small>${t("当前")}</small>` : ""}</td>
      <td class="num opt2">${esc(new Date(s.created).toLocaleString())}</td>
      <td><span class="badge ${esc(s.state)}">${esc(t(s.state))}</span>${s.memory ? ` <small>${t("含内存")}</small>` : ""}</td>
      <td class="wrap opt">${s.description ? esc(s.description) : `<span class="mute">–</span>`}</td>
      <td class="ops"><div><button data-i="${i}" data-a="revert"${op ? " disabled" : ""}>${icon("history")}${t("恢复")}</button><button class="del" data-i="${i}" data-a="delete"${op ? " disabled" : ""}>${icon("trash-2")}${t("删除")}</button></div></td>
    </tr>`).join("")}</tbody></table></div>`;
  el.querySelectorAll("td.ops button").forEach((b) => {
    const i = +b.dataset.i, s = ss[i];
    // Depth first: the ones after it that are deeper are taken from it.
    let kids = 0;
    for (let j = i + 1; j < ss.length && ss[j].depth > s.depth; j++) kids++;
    b.onclick = () => (b.dataset.a === "revert" ? revert(k, s) : remove(k, s, kids));
  });
}

function create(k) {
  const running = !off(k.info.state);
  formDialog({
    title: t("给 {name} 拍快照", { name: k.name }),
    body: `<label><span>${t("名称 (可选)")}</span><input name="name" placeholder="snap-…" spellcheck="false" autocomplete="off"></label>
      <label><span>${t("描述")}</span><textarea name="description" rows="3"></textarea></label>
      <p class="hint">${running ? t("运行中: 连同内存一起保存, 保存期间虚拟机暂停 (内存越大越久)。") : t("关机: 只保存磁盘。")}</p>`,
    ok: t("创建"),
    busy: t("创建中…"),
    submit: async (f) => {
      showEdited(await runOp(opKey(k.conn, k.uuid), t("给 {name} 拍快照", { name: k.name }),
        () => call("CreateSnapshot", k.conn, k.uuid, f.name.value.trim(), f.description.value)));
      loadMachine();
    },
  });
}

async function revert(k, s) {
  const text = s.memory
    ? t("把 {name} 恢复到快照 {snap}? 它会从拍快照那一刻继续运行; 之后的改动 (磁盘和内存) 都会丢失。", { name: k.name, snap: s.name })
    : t("把 {name} 恢复到快照 {snap}? 它会关机, 磁盘回到拍快照时的样子; 之后的改动都会丢失。", { name: k.name, snap: s.name });
  if (!(await confirmDanger(t("恢复快照"), text, t("恢复")))) return;
  try {
    showEdited(await runOp(opKey(k.conn, k.uuid), t("把 {name} 恢复到快照 {snap}", { name: k.name, snap: s.name }), () => call("RevertSnapshot", k.conn, k.uuid, s.name)));
  } catch (e) { fail(e); }
  loadMachine();
}

function remove(k, s, kids) {
  formDialog({
    title: t("删除快照 {snap}", { snap: s.name }),
    body: `<p>${esc(t(s.memory ? "删除 {name} 的快照 {snap}? 它保存的磁盘和内存状态会丢失, 再也不能恢复到那一刻; 虚拟机现在的状态不受影响。"
      : "删除 {name} 的快照 {snap}? 它保存的磁盘状态会丢失, 再也不能恢复到那一刻; 虚拟机现在的状态不受影响。", { name: k.name, snap: s.name }))}</p>
      ${kids ? `<label class="check"><input type="checkbox" name="children">${esc(t("连同从它派生的 {n} 个快照一起删除", { n: kids }))}</label>
        <p class="hint">${t("不勾选时, 从它派生的快照改挂到它的上一级。")}</p>` : ""}`,
    ok: t("删除"),
    danger: true,
    submit: async (f) => {
      showEdited(await runOp(opKey(k.conn, k.uuid), t("删除 {name} 的快照 {snap}", { name: k.name, snap: s.name }),
        () => call("DeleteSnapshot", k.conn, k.uuid, s.name, !!(f.children && f.children.checked))));
      loadMachine();
    },
  });
}
