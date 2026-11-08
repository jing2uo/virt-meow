// The details page's guest agent: what the guest says about itself (Info's
// guest, or why it can't: guest_note), and a user's password and SSH keys,
// as virt-mm agent does.
import { call } from "./rpc.js";
import { t } from "./i18n.js";
import { $, esc, errText, toast, confirmDanger, icon, skeleton } from "./util.js";
import { addTab, off, trackDirty } from "./machine.js";

const row = (k, v) => `<dt>${esc(k)}</dt><dd>${esc(v || "–")}</dd>`;

addTab({
  id: "agent",
  label: () => t("客户机代理"),
  html: (k) => {
    const m = k.info, g = m.guest;
    if (off(m.state)) return `<p class="mute">${t("客户机代理在客户机里运行: 开机后才能用。")}</p>`;
    const about = g ? `<dl class="kv">${row(t("主机名"), g.hostname)}${row(t("操作系统"), g.os)}
        ${row(t("内核"), g.kernel)}${row(t("架构"), g.arch)}</dl>`
      : `<p class="error">${esc(m.guest_note || t("客户机代理没有回应"))}</p>
        <p class="hint">${esc(t("客户机里要装并运行 qemu-guest-agent, 虚拟机要有它的通道 (<channel type='unix'><target type='virtio' name='org.qemu.guest_agent.0'/></channel>); virt-meow 新建的虚拟机都有这个通道。"))}</p>`;
    const dis = g ? "" : " disabled";
    return `<div class="cards">
      <div class="card"><h3>${t("客户机")}</h3>${about}</div>
      <form class="card form" id="f-pw" autocomplete="off">
        <h3>${t("设置用户密码")}</h3>
        <div class="grid">
          <label><span>${t("用户")}</span><input name="user" value="root" required spellcheck="false"${dis}></label>
          <label><span>${t("新密码")}</span><input name="pw" type="password" required autocomplete="new-password"${dis}></label>
        </div>
        <label><span>${t("再输一次")}</span><input name="pw2" type="password" required autocomplete="new-password"${dis}></label>
        <p class="hint">${t("通过客户机代理直接改客户机里的密码, 不需要知道旧密码。")}</p>
        <p class="error"></p>
        <div class="buttons"><button type="submit" class="primary"${dis}>${t("设置密码")}</button></div>
      </form>
    </div>
    <form class="card form" id="f-keys" autocomplete="off">
      <h3>${t("SSH 公钥")}</h3>
      <div class="inline"><label><span>${t("用户")}</span><input name="user" value="root" required spellcheck="false"${dis}></label>
        <button type="button" id="keys-load"${dis}>${t("读取")}</button></div>
      <div id="keys-list"></div>
      <label><span>${t("添加公钥 (每行一个)")}</span><textarea name="add" rows="3" class="mono" spellcheck="false" placeholder="ssh-ed25519 AAAA… user@host"${dis}></textarea></label>
      <p class="hint">${t("写进这个用户的 ~/.ssh/authorized_keys。")}</p>
      <p class="error"></p>
      <div class="buttons"><button type="submit" class="primary"${dis}>${t("添加")}</button></div>
    </form>`;
  },
  wire: (k, body) => {
    if (off(k.info.state) || !k.info.guest) return;
    const fp = $("#f-pw", body), fk = $("#f-keys", body);
    trackDirty(fp);
    trackDirty(fk);
    fp.onsubmit = async (e) => {
      e.preventDefault();
      const f = fp.elements, err = fp.querySelector(".error");
      err.textContent = "";
      if (f.pw.value !== f.pw2.value) { err.textContent = t("两次输入的密码不一样。"); return; }
      const user = f.user.value.trim();
      try {
        await call("SetUserPassword", k.conn, k.uuid, user, f.pw.value);
        toast(t("已设置 {user} 的密码", { user }));
        f.pw.value = f.pw2.value = "";
        fp.classList.remove("dirty");
      } catch (x) { err.textContent = errText(x); }
    };
    const list = $("#keys-list", body), kerr = fk.querySelector(".error");
    const load = async () => {
      const user = fk.elements.user.value.trim();
      kerr.textContent = "";
      list.innerHTML = skeleton();
      let keys;
      try { keys = await call("SSHKeys", k.conn, k.uuid, user); } catch (x) { list.innerHTML = ""; kerr.textContent = errText(x); return; }
      list.innerHTML = keys.length === 0 ? `<p class="mute">${esc(t("{user} 还没有 SSH 公钥", { user }))}</p>`
        : `<ul class="keys">${keys.map((x, i) => {
          const [type, , ...rest] = x.split(/\s+/);
          return `<li><span class="mono">${esc(type)}</span> <span>${esc(rest.join(" ") || t("(无注释)"))}</span>
            <small class="mono" title="${esc(x)}">${esc(x.split(/\s+/)[1]?.slice(-16) || "")}</small>
            <button type="button" class="del" data-i="${i}">${icon("trash-2")}${t("移除")}</button></li>`;
        }).join("")}</ul>`;
      list.querySelectorAll("button[data-i]").forEach((b) => {
        b.onclick = async () => {
          const key = keys[+b.dataset.i];
          if (!(await confirmDanger(t("移除公钥"), t("不再让 {user} 接受这个公钥登录?", { user }) + "\n" + key.slice(0, 80) + (key.length > 80 ? "…" : ""), t("移除")))) return;
          try { await call("SetSSHKeys", k.conn, k.uuid, user, [key], true); } catch (x) { kerr.textContent = errText(x); }
          load();
        };
      });
    };
    $("#keys-load", body).onclick = load;
    fk.onsubmit = async (e) => {
      e.preventDefault();
      const keys = fk.elements.add.value.split("\n").map((x) => x.trim()).filter((x) => x && !x.startsWith("#"));
      kerr.textContent = "";
      if (!keys.length) { kerr.textContent = t("先写上要添加的公钥。"); return; }
      try {
        await call("SetSSHKeys", k.conn, k.uuid, fk.elements.user.value.trim(), keys, false);
        fk.elements.add.value = "";
        fk.classList.remove("dirty");
        toast(t("已添加 {n} 个公钥", { n: keys.length }));
        load();
      } catch (x) { kerr.textContent = errText(x); }
    };
    load();
  },
});
