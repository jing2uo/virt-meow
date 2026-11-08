# osinfo-db, as virt-meow ships it

`os.tar.gz` is the `os/` directory of [osinfo-db](https://gitlab.com/libosinfo/osinfo-db)
20250619 (Debian's `osinfo-db` 0.20250606-1, `/usr/share/osinfo`), unmodified. virt-meow
reads a host's own osinfo-db when it has one (`/usr/share/osinfo`, over the
connection's SSH client or locally) and this copy otherwise (`internal/hv/osinfo.go`).

osinfo-db is licensed under the GNU General Public License, version 2 or later
(`COPYING`); Copyright (C) 2016 Red Hat, Inc and its contributors.

To update it, from a current osinfo-db (a distribution's package, or a release tarball
from https://releases.pagure.org/libosinfo/):

    tar -C /usr/share/osinfo -czf internal/hv/osinfo-db/os.tar.gz os

and change the version above.
