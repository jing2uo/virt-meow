// Keys for a VNC guest that the desktop would keep for itself (Ctrl+Alt+Del,
// Ctrl+Alt+F1…), and text typed into a guest key by key, for pasting where
// there is no agent to take a clipboard.
import KeyTable from "./vendor/novnc/core/input/keysym.js";
import keysyms from "./vendor/novnc/core/input/keysymdef.js";

const ctrl = [KeyTable.XK_Control_L, "ControlLeft"], alt = [KeyTable.XK_Alt_L, "AltLeft"];

// combos are the send-key menu's, in its order: [id, label, keys].
export const combos = [
  ["cad", "Ctrl+Alt+Del", [ctrl, alt, [KeyTable.XK_Delete, "Delete"]]],
  ["cab", "Ctrl+Alt+Backspace", [ctrl, alt, [KeyTable.XK_BackSpace, "Backspace"]]],
  ...Array.from({ length: 12 }, (_, i) => [`f${i + 1}`, `Ctrl+Alt+F${i + 1}`, [ctrl, alt, [KeyTable.XK_F1 + i, `F${i + 1}`]]]),
  ["print", "PrintScreen", [[KeyTable.XK_Print, "PrintScreen"]]],
];

// sendCombo presses a combo's keys in order and lets go of them backwards.
export function sendCombo(rfb, id) {
  const keys = combos.find((c) => c[0] === id)?.[2];
  if (!keys) return;
  for (const [sym, code] of keys) rfb.sendKey(sym, code, true);
  for (const [sym, code] of [...keys].reverse()) rfb.sendKey(sym, code, false);
}

// A US keyboard: the key for each character it types, and whether Shift
// is held. QEMU takes the key (its scancode) over the keysym, so the guest
// sees these keys as its own layout reads them.
const plain = { "-": "Minus", "=": "Equal", "[": "BracketLeft", "]": "BracketRight", "\\": "Backslash", ";": "Semicolon",
  "'": "Quote", ",": "Comma", ".": "Period", "/": "Slash", "`": "Backquote", " ": "Space", "\n": "Enter", "\t": "Tab" };
const shifted = { "!": "Digit1", "@": "Digit2", "#": "Digit3", "$": "Digit4", "%": "Digit5", "^": "Digit6", "&": "Digit7",
  "*": "Digit8", "(": "Digit9", ")": "Digit0", "_": "Minus", "+": "Equal", "{": "BracketLeft", "}": "BracketRight",
  "|": "Backslash", ":": "Semicolon", '"': "Quote", "<": "Comma", ">": "Period", "?": "Slash", "~": "Backquote" };

function keyOf(ch) {
  if (/^[a-z]$/.test(ch)) return { code: "Key" + ch.toUpperCase(), shift: false };
  if (/^[A-Z]$/.test(ch)) return { code: "Key" + ch, shift: true };
  if (/^[0-9]$/.test(ch)) return { code: "Digit" + ch, shift: false };
  if (plain[ch]) return { code: plain[ch], shift: false };
  if (shifted[ch]) return { code: shifted[ch], shift: true };
  return null;
}

// typable splits text into what a US keyboard types and the characters it
// can't (and are left out).
export function typable(text) {
  const keys = [], skipped = new Set();
  for (const ch of text.replace(/\r\n?/g, "\n")) {
    const k = keyOf(ch);
    if (!k) { skipped.add(ch); continue; }
    const sym = ch === "\n" ? KeyTable.XK_Return : ch === "\t" ? KeyTable.XK_Tab : keysyms.lookup(ch.codePointAt(0));
    keys.push({ ...k, sym });
  }
  return { keys, skipped: [...skipped] };
}

// typeKeys types keys into the guest, a few at a time: a guest's PS/2
// keyboard drops what comes faster than it reads. progress(n) after each
// batch; it stops early when stop() says so.
export async function typeKeys(rfb, keys, progress = () => {}, stop = () => false) {
  const shift = [KeyTable.XK_Shift_L, "ShiftLeft"];
  for (let i = 0; i < keys.length; i++) {
    if (stop()) return i;
    const k = keys[i];
    if (k.shift) rfb.sendKey(...shift, true);
    rfb.sendKey(k.sym, k.code, true);
    rfb.sendKey(k.sym, k.code, false);
    if (k.shift) rfb.sendKey(...shift, false);
    if (i % 8 === 7) {
      progress(i + 1);
      await new Promise((r) => setTimeout(r, 40));
    }
  }
  progress(keys.length);
  return keys.length;
}
