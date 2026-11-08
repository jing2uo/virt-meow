// How the page talks to Go: api.Service's methods and its events. In the
// window that is Wails' runtime; in a browser under virt-mm serve, a POST per
// call and one WebSocket for the events (internal/web). Nothing else in the
// page knows which.

let wails = null;
try { wails = await import("/wails/runtime.js"); } catch { /* a browser: virt-mm serve */ }

export const web = !wails;

const S = "github.com/jing2uo/virt-meow/internal/api.Service.";

// called, when set, hears of every call and its outcome (err null when it
// succeeded): the activity log's actions (activity.js).
let called = null;
export function onCall(fn) { called = fn; }

// call runs a Service method; a failure rejects with an Error whose message
// is Go's.
export async function call(method, ...args) {
  try {
    const r = await rawCall(method, args);
    called?.(method, args, null);
    return r;
  } catch (e) {
    called?.(method, args, e);
    throw e;
  }
}

async function rawCall(method, args) {
  if (wails) return wails.Call.ByName(S + method, ...args);
  const r = await fetch("/api/" + method, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(args),
  });
  const body = await r.json().catch(() => null);
  // The session ended (logged out elsewhere, or serve restarted): log in.
  if (r.status === 401 && location.pathname !== "/login") location.href = "/login";
  if (!r.ok) throw new Error((body && body.error) || `${method}: HTTP ${r.status}`);
  return body;
}

const listeners = new Map(); // event name -> functions

// on calls fn with each event's data. Under serve, "virt-mm:reconnected" also
// comes after the event stream was lost and is back: events were missed, so
// the page reloads what it shows.
export function on(name, fn) {
  if (wails) {
    wails.Events.On(name, (ev) => fn(ev.data));
    return;
  }
  if (!listeners.has(name)) listeners.set(name, []);
  listeners.get(name).push(fn);
}

function dispatch(name, data) {
  for (const fn of listeners.get(name) || []) {
    try { fn(data); } catch (e) { console.error(e); }
  }
}

if (!wails) {
  let wait = 500, lost = false;
  const open = () => {
    const ws = new WebSocket(wsURL("/api/events"));
    ws.onopen = () => {
      wait = 500;
      if (lost) dispatch("virt-mm:reconnected");
      lost = false;
    };
    ws.onmessage = (m) => {
      const ev = JSON.parse(m.data);
      dispatch(ev.name, ev.data);
    };
    ws.onclose = () => {
      lost = true;
      setTimeout(open, wait);
      wait = Math.min(wait * 2, 10000);
    };
  };
  open();
}

// wsURL makes a WebSocket URL of a path (or keeps a ws:// URL as it is),
// against the page's own address.
export function wsURL(u) {
  const url = new URL(u, location.href);
  if (url.protocol === "http:") url.protocol = "ws:";
  if (url.protocol === "https:") url.protocol = "wss:";
  return url.href;
}

// upload sends a file the user picked in the browser into pool; progress
// comes as "upload" events. Only under serve: the window picks files
// itself (PickUpload).
export async function upload(connection, pool, file) {
  const q = new URLSearchParams({ connection, pool, name: file.name, size: String(file.size) });
  const r = await fetch("/api/upload?" + q, {
    method: "POST",
    headers: { "Content-Type": "application/octet-stream" },
    body: file,
  });
  if (!r.ok) {
    const body = await r.json().catch(() => null);
    throw new Error((body && body.error) || `upload: HTTP ${r.status}`);
  }
}
