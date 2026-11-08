# Vendored front-end code

Copied unchanged from their releases; the page imports them as ES modules,
without a bundler (docs/adr/0001). Update by copying a newer release over
the same files.

| Directory | What | Version | Licence |
|---|---|---|---|
| `novnc/core`, `novnc/vendor/pako` | [noVNC](https://github.com/novnc/noVNC): the VNC client, its `core/` only, with pako (zlib) | v1.7.0 | MPL-2.0 (`novnc/LICENSE.txt`, `novnc/docs/`); pako MIT (`novnc/vendor/pako/LICENSE`) |
| `spice` | [spice-html5](https://gitlab.freedesktop.org/spice/spice-html5): the SPICE client, its `src/` without the ES module loader for old browsers | 0.3.0 | LGPL-3.0 (`spice/COPYING.LESSER`, `spice/COPYING`); `spice/thirdparty` BSD-style and public domain, as each file says |
| `xterm` | [xterm.js](https://github.com/xtermjs/xterm.js): the terminal for the serial console, `lib/xterm.mjs` and `css/xterm.css` of `@xterm/xterm`, and `lib/addon-fit.mjs` of `@xterm/addon-fit` (in `addon-fit/`) | 6.0.0, addon-fit 0.11.0 | MIT (`xterm/LICENSE`, `xterm/addon-fit/LICENSE`) |
| `lucide` | [Lucide](https://lucide.dev): the icons the page uses, as SVG files from `lucide-static`'s `icons/` (style.css lists each as a class) | 1.63.0 | ISC, some icons MIT from Feather (`lucide/LICENSE`) |
