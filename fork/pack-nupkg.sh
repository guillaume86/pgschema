#!/usr/bin/env bash
# merp fork: packs bin/<rid>/ (from fork/build-rids.sh) as Merp.Pgschema.<version>.nupkg into a folder feed.
# Env: MERP_VERSION (default 0.1.0), MERP_FEED (default ~/.merp/feed). Needs only python3 (standard library).
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
exec python3 -I - "$root" "${MERP_VERSION:-0.1.0}" "${MERP_FEED:-$HOME/.merp/feed}" <<'PY'
import os, sys, zipfile
root, version, feed = sys.argv[1:4]
rids = ["linux-x64", "linux-arm64", "osx-arm64", "win-x64"]
os.makedirs(feed, exist_ok=True)
out = os.path.join(feed, f"Merp.Pgschema.{version}.nupkg")
nuspec = f"""<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://schemas.microsoft.com/packaging/2013/05/nuspec.xsd">
  <metadata>
    <id>Merp.Pgschema</id>
    <version>{version}</version>
    <authors>merp</authors>
    <description>The merp fork of pgschema (the DDL engine), one binary per RID under runtimes/&lt;rid&gt;/native/.</description>
    <license type="expression">Apache-2.0</license>
  </metadata>
</package>
"""
ctypes = """<?xml version="1.0" encoding="utf-8"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml" />
  <Default Extension="nuspec" ContentType="application/octet" />
  <Default Extension="exe" ContentType="application/octet" />
  <Override PartName="/runtimes/linux-x64/native/pgschema" ContentType="application/octet" />
  <Override PartName="/runtimes/linux-arm64/native/pgschema" ContentType="application/octet" />
  <Override PartName="/runtimes/osx-arm64/native/pgschema" ContentType="application/octet" />
</Types>
"""
rels = """<?xml version="1.0" encoding="utf-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Type="http://schemas.microsoft.com/packaging/2010/07/manifest" Target="/Merp.Pgschema.nuspec" Id="R1" />
</Relationships>
"""
tmp = out + ".tmp"
with zipfile.ZipFile(tmp, "w", zipfile.ZIP_DEFLATED) as z:
    z.writestr("[Content_Types].xml", ctypes)
    z.writestr("_rels/.rels", rels)
    z.writestr("Merp.Pgschema.nuspec", nuspec)
    for rid in rids:
        name = "pgschema.exe" if rid.startswith("win") else "pgschema"
        src = os.path.join(root, "bin", rid, name)
        info = zipfile.ZipInfo(f"runtimes/{rid}/native/{name}", (2024, 1, 1, 0, 0, 0))
        info.external_attr = 0o755 << 16
        info.compress_type = zipfile.ZIP_DEFLATED
        with open(src, "rb") as f:
            z.writestr(info, f.read())
os.replace(tmp, out)
print(out)
PY
