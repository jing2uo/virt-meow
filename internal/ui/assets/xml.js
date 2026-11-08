// The XML editor: a definition in a dialog (#dlg-xml), saved only when
// libvirt takes it and it hasn't changed on the host meanwhile; and the
// helpers the creation form's XML step shares with it.
import { t } from "./i18n.js";
import { $, confirmDanger } from "./util.js";

// xmlProblem is the first well-formedness error in x, or "": caught here,
// with its line and column, before libvirt sees the document.
export function xmlProblem(x) {
  const err = new DOMParser().parseFromString(x, "application/xml").querySelector("parsererror");
  if (!err) return "";
  return t("XML 格式错误: {err}", { err: (err.querySelector("div")?.textContent || err.textContent).trim() });
}

// editorKeys: Tab indents (Shift+Tab still leaves the field), Ctrl+S saves.
export function editorKeys(ta, save) {
  ta.onkeydown = (e) => {
    if (e.key === "Tab" && !e.shiftKey && !e.ctrlKey && !e.altKey && !e.metaKey) {
      e.preventDefault();
      document.execCommand("insertText", false, "  "); // keeps the undo history, unlike setting value
    } else if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "s") {
      e.preventDefault();
      save();
    }
  };
}

export const discard = () => confirmDanger(t("放弃修改"), t("XML 的修改还没保存, 放弃吗?"), t("放弃"));

// guardClose asks before Esc closes a dialog holding unsaved edits.
export function guardClose(dlg, dirty) {
  dlg.addEventListener("cancel", async (e) => {
    if (!dirty()) return;
    e.preventDefault();
    if (await discard()) dlg.close("cancel");
  });
}

// edit is what the editor holds: the spec it was opened with (load() and
// save(def, xml) give a definition { name, xml, active, … }; title(def),
// note(def) and saved(def) say what it is and what saving did) and the
// definition as read, which saving checks is still current.
let edit = null;
const dx = $("#dlg-xml"), fx = $("#form-xml");
const dirty = () => edit && fx.elements.xml.value !== edit.def.xml;

function show(def) {
  edit.def = def;
  $("#xml-title").textContent = edit.title(def);
  $("#xml-note").textContent = edit.note(def);
  fx.elements.xml.value = def.xml;
  $("#xml-error").textContent = "";
}

// editXML opens the editor on what spec.load() reads; a failed read
// rejects, with nothing opened.
export async function editXML(spec) {
  const def = await spec.load();
  edit = { ...spec, def };
  show(def);
  $("#xml-save").disabled = false;
  dx.returnValue = "";
  dx.showModal();
  fx.elements.xml.focus();
  fx.elements.xml.setSelectionRange(0, 0);
  fx.elements.xml.scrollTop = 0; // the caret alone doesn't scroll it
}

editorKeys(fx.elements.xml, () => fx.requestSubmit());
guardClose(dx, dirty);
$("#xml-cancel").onclick = async () => {
  if (dirty() && !(await discard())) return;
  dx.close("cancel");
};
$("#xml-reload").onclick = async () => {
  if (dirty() && !(await discard())) return;
  try { show(await edit.load()); } catch (e) { $("#xml-error").textContent = String(e.message || e); }
};
fx.addEventListener("submit", async (e) => {
  e.preventDefault();
  const x = fx.elements.xml.value;
  const p = xmlProblem(x);
  if (p) { $("#xml-error").textContent = p; return; }
  if (!dirty()) { dx.close("ok"); return; }
  const btn = $("#xml-save");
  btn.disabled = true;
  $("#xml-error").textContent = "";
  try {
    const def = await edit.save(edit.def, x);
    dx.close("ok");
    edit.saved(def);
  } catch (err) {
    $("#xml-error").textContent = String(err.message || err).trim();
  }
  btn.disabled = false;
});
