// page-shot.mjs <debug port> <url> <png> <wait ms> <expression>: run by
// page-shot.sh. Opens url, lets it run for wait ms, saves a screenshot and
// prints the page's exceptions, console errors, failed requests and the
// expression's value.
// SHOT_SIZE=WxH sets the window (1280x800); SHOT_SCHEME=dark (or light)
// emulates prefers-color-scheme; SHOT_LANG=en-US the browser's language; SHOT_DO is JS run after the wait (clicks,
// say, as a user gesture), then SHOT_DO_WAIT ms (1500) pass; SHOT_KEYS are
// pressed then (see below), before the screenshot.
import { writeFileSync } from "node:fs";
const [port, url, png, wait, expr] = process.argv.slice(2);
const t = await (await fetch(`http://127.0.0.1:${port}/json/new?${encodeURIComponent("about:blank")}`, { method: "PUT" })).json();
const ws = new WebSocket(t.webSocketDebuggerUrl);
let id = 0;
const pending = new Map();
const send = (method, params = {}) => new Promise((res) => { pending.set(++id, res); ws.send(JSON.stringify({ id, method, params })); });
ws.onmessage = (m) => {
  const d = JSON.parse(m.data);
  if (d.id && pending.has(d.id)) { pending.get(d.id)(d.result); pending.delete(d.id); }
  if (d.method === "Runtime.exceptionThrown") console.log("exception:", d.params.exceptionDetails.exception?.description || d.params.exceptionDetails.text);
  if (d.method === "Runtime.consoleAPICalled" && d.params.type === "error") console.log("console.error:", d.params.args.map((a) => a.value ?? a.description).join(" "));
  // A file the page asked for and didn't get (a module missing from a
  // build, say), and what the browser itself logs as an error.
  if (d.method === "Network.responseReceived" && d.params.response.status >= 400) console.log(`http ${d.params.response.status}:`, d.params.response.url);
  if (d.method === "Network.loadingFailed" && !d.params.canceled) console.log("load failed:", d.params.errorText, d.params.type);
  if (d.method === "Log.entryAdded" && d.params.entry.level === "error") console.log("log.error:", d.params.entry.text, d.params.entry.url || "");
};
await new Promise((r) => (ws.onopen = r));
await send("Runtime.enable");
await send("Network.enable");
await send("Log.enable");
const [width, height] = (process.env.SHOT_SIZE || "1280x800").split("x").map(Number);
await send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: 1, mobile: false });
if (process.env.SHOT_SCHEME) await send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: process.env.SHOT_SCHEME }] });
if (process.env.SHOT_LANG) {
  await send("Emulation.setLocaleOverride", { locale: process.env.SHOT_LANG });
  const ua = (await send("Browser.getVersion")).userAgent;
  await send("Emulation.setUserAgentOverride", { userAgent: ua, acceptLanguage: process.env.SHOT_LANG });
}
// Full screen, which the console's keyboard grab asks for, is otherwise
// "not granted" in headless Chrome (seen in 156): grant it, as a user would.
const fs = await send("Browser.setPermission", { permission: { name: "fullscreen", allowWithoutGesture: true }, setting: "granted", origin: new URL(url).origin });
if (!fs) console.log("page-shot: could not grant full screen");
await send("Page.navigate", { url });
await new Promise((r) => setTimeout(r, +wait));
if (process.env.SHOT_DO) {
  const r = await send("Runtime.evaluate", { expression: process.env.SHOT_DO, awaitPromise: true, userGesture: true });
  if (r.exceptionDetails) console.log("SHOT_DO:", r.exceptionDetails.exception?.description || r.exceptionDetails.text);
  await new Promise((r) => setTimeout(r, +(process.env.SHOT_DO_WAIT || 1500)));
}
// SHOT_KEYS: keys pressed as a user would, after SHOT_DO: "Tab S-Tab Enter
// Escape Space ArrowDown", S- holding Shift; +Control and -Control press
// and let go of a modifier (Control, Alt, Meta) on its own, for chords.
const keys = { Tab: 9, Enter: 13, Escape: 27, Space: 32, ArrowDown: 40, ArrowUp: 38 };
const mods = { Alt: [18, 1, "AltLeft"], Control: [17, 2, "ControlLeft"], Meta: [91, 4, "MetaLeft"] };
let held = 0;
for (const k of (process.env.SHOT_KEYS || "").split(/\s+/).filter(Boolean)) {
  if (/^[+-]/.test(k) && mods[k.slice(1)]) {
    const [code, bit, name] = mods[k.slice(1)], down = k[0] === "+";
    held = down ? held | bit : held & ~bit;
    await send("Input.dispatchKeyEvent", { type: down ? "rawKeyDown" : "keyUp", key: k.slice(1), code: name,
      windowsVirtualKeyCode: code, nativeVirtualKeyCode: code, modifiers: held });
    await new Promise((r) => setTimeout(r, 150));
    continue;
  }
  const shift = k.startsWith("S-"), key = shift ? k.slice(2) : k, code = keys[key];
  const ev = { key: key === "Space" ? " " : key, code: key, windowsVirtualKeyCode: code, nativeVirtualKeyCode: code, modifiers: shift ? 8 : 0 };
  await send("Input.dispatchKeyEvent", { type: "rawKeyDown", ...ev, ...(key === "Enter" ? { text: "\r" } : key === "Space" ? { text: " " } : {}) });
  if (key === "Enter" || key === "Space") await send("Input.dispatchKeyEvent", { type: "char", ...ev, text: key === "Enter" ? "\r" : " " });
  await send("Input.dispatchKeyEvent", { type: "keyUp", ...ev });
  await new Promise((r) => setTimeout(r, 250));
}
if (process.env.SHOT_KEYS) await new Promise((r) => setTimeout(r, 800));
const shot = await send("Page.captureScreenshot", { format: "png" });
writeFileSync(png, Buffer.from(shot.data, "base64"));
const v = await send("Runtime.evaluate", { expression: expr, returnByValue: true, awaitPromise: true });
console.log("value:", JSON.stringify(v.result?.value ?? v.exceptionDetails?.text));
ws.close();
