// The page's keyboard shortcuts: Ctrl+N a new machine, Ctrl+F the list's
// search, Esc back out of a page (a dialog closes on its own), ? lists
// them. Keys typed into a console's screen are the guest's, never these.
import { t } from "./i18n.js";
import { esc, infoDialog } from "./util.js";

const shortcuts = () => [
  ["Ctrl+N", t("新建虚拟机")],
  ["Ctrl+F", t("搜索列表")],
  ["Esc", t("返回: 离开虚拟机页面、控制台、对话框或面板")],
  ["?", t("显示这些快捷键")],
];

export function showShortcuts() {
  infoDialog(t("快捷键"), `<dl class="kv keys">${shortcuts().map(([k, what]) => `<dt><kbd>${esc(k)}</kbd></dt><dd>${esc(what)}</dd>`).join("")}</dl>
    <p class="hint">${esc(t("在控制台的画面里, 按键都交给客户机。"))}</p>`);
}

// h: newMachine(), search() and back(), each true when it did something.
export function initKeys(h) {
  document.addEventListener("keydown", (e) => {
    if (e.defaultPrevented || e.isComposing) return;
    const el = e.target instanceof Element ? e.target : null;
    if (el?.closest("#screen")) return; // the guest's keys
    const typing = el?.closest("input, textarea, select, [contenteditable]");
    const mod = (e.ctrlKey || e.metaKey) && !e.altKey && !e.shiftKey;
    const key = e.key.toLowerCase();
    if (mod && key === "n") { if (!document.querySelector("dialog[open]") && h.newMachine()) e.preventDefault(); return; }
    if (mod && key === "f") { if (!document.querySelector("dialog[open]") && h.search()) e.preventDefault(); return; }
    if (document.querySelector("dialog[open]")) return; // Esc closes it, as dialogs do
    if (e.key === "Escape") {
      if (typing && el.value) return; // a search field: Esc clears it first
      if (typing) el.blur();
      if (h.back()) e.preventDefault();
      return;
    }
    if (e.key === "?" && !typing && !e.ctrlKey && !e.metaKey && !e.altKey) {
      e.preventDefault();
      showShortcuts();
    }
  });
}
