"""Verify that only the official glibc package family changed after backport qualification."""
import json
import sys
from pathlib import Path

PACKAGES = {"libc6", "libc-bin", "libc-gconv-modules-extra"}
VERSION = "2.43-2ubuntu2.7"
PREFIX = "pkg:deb/ubuntu/"


def os_packages(path):
    report = json.loads(path.read_bytes())
    if report.get("bomFormat") != "CycloneDX":
        raise ValueError("CycloneDX SBOM required")
    packages = {}
    for component in report["components"]:
        purl = component.get("purl", "")
        if not purl.startswith(PREFIX):
            continue
        name = component["name"]
        if name in packages:
            raise ValueError("Duplicate Ubuntu package: " + name)
        packages[name] = (component["version"], purl)
    return packages


def verify(before, after):
    if not PACKAGES <= before.keys() or before.keys() != after.keys():
        raise ValueError("Installed Ubuntu package set changed")
    for name, original in before.items():
        expected = (
            (VERSION, f"{PREFIX}{name}@{VERSION}?arch=amd64&distro=ubuntu-26.04")
            if name in PACKAGES else original
        )
        if after[name] != expected:
            raise ValueError("Unexpected Ubuntu package identity: " + name)


if __name__ == "__main__":
    before, after = map(Path, sys.argv[1:])
    verify(os_packages(before), os_packages(after))
    print("OFFICIAL_GLIBC_SBOM_CLOSURE_OK")
