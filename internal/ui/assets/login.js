// virt-mm serve's login page: the one password, then the page itself. It
// loads before there is a session, so it knows only i18n.js and the server's
// /api/login (internal/web).
import { t, translate } from "./i18n.js";

translate();
const f = document.getElementById("login");
const err = document.getElementById("login-error");
if (new URLSearchParams(location.search).has("out")) document.getElementById("login-note").textContent = t("已退出登录。");

// A server without a login has no use for this page.
fetch("/api/session").then((r) => r.json()).then((s) => { if (!s.login) location.href = "/"; }).catch(() => {});

f.addEventListener("submit", async (e) => {
  e.preventDefault();
  err.textContent = "";
  const btn = f.querySelector("button");
  btn.disabled = true;
  try {
    const r = await fetch("/api/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ password: f.elements.password.value }),
    });
    if (r.ok) {
      location.href = "/";
      return;
    }
    if (r.status === 429) err.textContent = t("失败次数太多, 请 {n} 秒后再试。", { n: r.headers.get("Retry-After") || 60 });
    else if (r.status === 401) err.textContent = t("密码不对。");
    else err.textContent = (await r.json().catch(() => ({}))).error || `HTTP ${r.status}`;
    f.elements.password.select();
  } catch (e2) {
    err.textContent = String(e2.message || e2);
  }
  btn.disabled = false;
});
