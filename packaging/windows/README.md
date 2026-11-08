# Windows resources

`winres.json` is what `make winres` turns into `cmd/virt-mm/rsrc_windows_*.syso`
with [go-winres](https://github.com/tc-hib/go-winres), linked into Windows
builds only:

- the icon, from the Flatpak's PNG, as two icon groups: `#3`, the one Wails'
  own resource generator writes, and `#32512` (`IDI_APPLICATION`), the one
  Wails v3 loads for its windows; Explorer shows the first;
- the manifest: common controls v6, per-monitor DPI awareness, long paths;
- the version (Details tab), from `VERSION` (`scripts/version.sh`).
