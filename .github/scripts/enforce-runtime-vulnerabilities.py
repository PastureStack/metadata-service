"""Enforce release findings; only exact, reviewed unfixed low/medium entries may pass."""
import argparse
import json
from pathlib import Path


def violations(report, ledger, repository):
    if ledger.get("repository") != repository:
        raise ValueError("allowlist repository mismatch")
    reviewed = {}
    for entry in ledger.get("unfixed", []):
        key = (entry["id"], entry["package"], entry["version"], entry.get("purl", ""))
        if key in reviewed:
            raise ValueError("duplicate reviewed finding")
        if (entry.get("severity") not in ("LOW", "MEDIUM")
                or entry.get("status") != "no_official_fix"
                or not entry.get("officialEvidence", "").startswith("https://")
                or not entry.get("reason") or not entry.get("checkedAt")):
            raise ValueError("incomplete official no-fix evidence")
        reviewed[key] = entry
    errors = []
    for result in report.get("Results") or []:
        if result.get("Secrets"):
            errors.append("secret findings remain")
        for finding in result.get("Vulnerabilities") or []:
            severity = finding.get("Severity", "UNKNOWN")
            key = (finding.get("VulnerabilityID"), finding.get("PkgName"),
                   finding.get("InstalledVersion"),
                   (finding.get("PkgIdentifier") or {}).get("PURL", ""))
            entry = reviewed.get(key)
            fixed = str(finding.get("FixedVersion") or "").strip()
            if (severity not in ("LOW", "MEDIUM") or fixed
                    or entry is None or entry["severity"] != severity):
                errors.append("%s %s@%s severity=%s fixed=%s" % (
                    key[0], key[1], key[2], severity, fixed or "none"))
    return errors


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("report")
    parser.add_argument("ledger")
    parser.add_argument("--repository", required=True)
    args = parser.parse_args()
    failures = violations(json.loads(Path(args.report).read_text()),
                          json.loads(Path(args.ledger).read_text()), args.repository)
    if failures:
        raise SystemExit("Release vulnerability policy failed:\n" + "\n".join(failures))
    print("RELEASE_VULNERABILITY_POLICY_OK")
