// The serial console's client: xterm.js on the console bridge's WebSocket,
// which carries the console's bytes both ways (api.OpenSerial). Why it
// couldn't open, or that it ended, comes as the terminal's last line.
import { Terminal } from "./vendor/xterm/lib/xterm.mjs";
import { FitAddon } from "./vendor/xterm/addon-fit/lib/addon-fit.mjs";

let css = false;

// serialClient connects a terminal in screen to url; onEnd(clean, text)
// gets whether the page closed it and the last bytes it showed.
export function serialClient(screen, url, { onOpen, onEnd }) {
  if (!css) {
    const l = document.createElement("link");
    l.rel = "stylesheet";
    l.href = new URL("./vendor/xterm/css/xterm.css", import.meta.url).href;
    document.head.append(l);
    css = true;
  }
  const term = new Terminal({
    cursorBlink: true, scrollback: 5000, convertEol: false,
    fontFamily: '"JetBrains Mono", "Noto Sans Mono CJK SC", ui-monospace, monospace', fontSize: 14,
    theme: { background: "#000000", black: "#000000" }, // firmware paints with ANSI black
  });
  const fit = new FitAddon();
  term.loadAddon(fit);
  term.open(screen);
  fit.fit();
  const resize = new ResizeObserver(() => { try { fit.fit(); } catch { /* gone */ } });
  resize.observe(screen);

  const ws = new WebSocket(url, ["binary"]);
  ws.binaryType = "arraybuffer";
  const enc = new TextEncoder(), dec = new TextDecoder();
  let tail = "", closing = false;
  ws.onopen = () => { onOpen(); term.focus(); };
  ws.onmessage = (m) => {
    const b = new Uint8Array(m.data);
    term.write(b);
    tail = (tail + dec.decode(b, { stream: true })).slice(-400);
  };
  ws.onclose = () => { resize.disconnect(); onEnd(closing, tail); };
  term.onData((s) => { if (ws.readyState === WebSocket.OPEN) ws.send(enc.encode(s)); });
  term.onBinary((s) => {
    if (ws.readyState === WebSocket.OPEN) ws.send(Uint8Array.from(s, (c) => c.charCodeAt(0) & 0xff));
  });
  return {
    close() { closing = true; ws.close(); term.dispose(); },
    focus() { term.focus(); },
  };
}
