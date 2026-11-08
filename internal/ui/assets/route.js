// Where the page is, as its URL's hash says it, so that Back and Forward
// move between views and a reload or a second window lands in the same
// place:
//   #/all                                   every host
//   #/<conn>[/machines|storage|networks|host]  a connection's tab
//   #/<conn>/machine/<name>[/<tab>]         a machine's page
//   #/<conn>/console/<name>[/<kind>]        its console (vnc, spice, serial)
// and the older ones virt-mm gui opens with, on the first connection:
// #console/<name>[/<kind>], #machine/<name> and #create.

const kinds = "(vnc|spice|serial)";
const forms = [
  [/^\/all$/, () => ({ all: true })],
  [new RegExp(`^/([^/]+)/console/([^/]+)(?:/${kinds})?$`), (m) => ({ conn: m[1], console: m[2], kind: m[3] || "" })],
  [/^\/([^/]+)\/machine\/([^/]+)(?:\/([a-z-]+))?$/, (m) => ({ conn: m[1], machine: m[2], tab: m[3] || "overview" })],
  [/^\/([^/]+)(?:\/(machines|storage|networks|host))?$/, (m) => ({ conn: m[1], tab: m[2] || "machines" })],
  [new RegExp(`^console/([^/]+)(?:/${kinds})?$`), (m) => ({ conn: "", console: m[1], kind: m[2] || "" })],
  [/^machine\/([^/]+)$/, (m) => ({ conn: "", machine: m[1], tab: "overview" })],
  [/^create$/, () => ({ conn: "", create: true })],
];

// parse reads a hash; null when it names no view.
export function parse(hash) {
  const h = hash.replace(/^#/, "");
  for (const [re, view] of forms) {
    const m = h.match(re);
    if (!m) continue;
    try {
      return view(m.map((x) => (x === undefined ? x : decodeURIComponent(x))));
    } catch { return null; } // a broken %-escape
  }
  return null;
}

// format writes a view as a hash, "#" included; "" for none.
export function format(v) {
  if (!v) return "";
  if (v.all) return "#/all";
  const e = encodeURIComponent, c = "#/" + e(v.conn);
  if (v.console) return `${c}/console/${e(v.console)}${v.kind ? "/" + v.kind : ""}`;
  if (v.machine) return `${c}/machine/${e(v.machine)}${v.tab && v.tab !== "overview" ? "/" + v.tab : ""}`;
  return v.tab && v.tab !== "machines" ? `${c}/${v.tab}` : c;
}
