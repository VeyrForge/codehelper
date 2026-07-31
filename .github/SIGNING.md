# Release signing and provenance

Wired in `workflows/release.yml`'s `release` job. Both mechanisms are **keyless**
(Sigstore OIDC via `id-token: write`) — no signing key, password, or secret is
stored in this repository.

## What runs on every tagged release

1. **SBOM** (`anchore/sbom-action`) — CycloneDX (`sbom.cdx.json`) and SPDX
   (`sbom.spdx.json`), attached as release assets.
2. **Build provenance attestation** (`actions/attest@v4`) — binds each release
   archive's + SBOM's digest to this workflow run (repo, ref, commit, workflow
   file) via GitHub's Attestations API. Verify with the GitHub CLI:

   ```sh
   gh attestation verify dist/codehelper_<version>_linux_amd64.tar.gz \
     --owner VeyrForge
   ```

3. **Cosign keyless signing** (`sigstore/cosign-installer` + `cosign sign-blob`)
   — signs `checksums.txt` (which lists every published archive's SHA-256), so
   one signature + Sigstore bundle (`checksums.txt.sigstore.json`) covers the
   whole release. Verify with:

   ```sh
   cosign verify-blob dist/checksums.txt \
     --bundle dist/checksums.txt.sigstore.json \
     --certificate-identity "https://github.com/VeyrForge/codehelper/.github/workflows/release.yml@refs/tags/<tag>" \
     --certificate-oidc-issuer "https://token.actions.githubusercontent.com"
   # then confirm a downloaded archive matches:
   sha256sum -c <(grep codehelper_<version>_linux_amd64.tar.gz dist/checksums.txt)
   ```

## Installer verification (`scripts/install.sh` / `install.ps1`)

| Layer | When | Behavior |
|---|---|---|
| **SHA-256 vs `checksums.txt`** | Always | Mandatory. Install fails closed on missing/mismatch. |
| **Cosign `verify-blob`** | `cosign` on PATH **and** release publishes `checksums.txt.sigstore.json` | Preferred. Failure fails the install. Absent tool/asset → honest skip. |
| **`gh attestation verify`** | `gh` on PATH **and** Attestations API returns a subject for the archive digest | Preferred. Failure fails the install. Absent tool/attestation → honest skip. |

**Current published reality (through at least v3.0.3 on VeyrForge):** release
assets include `checksums.txt` and binaries only — no `checksums.txt.sigstore.json`,
no SBOM assets, and no attestation subjects for archive digests. Installers
therefore run **checksum-only** today and print that optional layers were
skipped. They do **not** claim attestation/cosign success without a real verify.

The release workflow above is wired so the **next** cut that publishes those
assets activates the optional layers automatically — no installer change needed.

Env overrides:

- `CODEHELPER_SKIP_ATTESTATION=1` — skip optional cosign/attestation (checksum still required).
- `CODEHELPER_REQUIRE_ATTESTATION=1` — fail if neither cosign nor `gh attestation` confirmed (useful once releases publish them).

## Why this is safe to run with no local/CI secrets

- `id-token: write` lets the job request a short-lived GitHub Actions OIDC
  token; cosign and `actions/attest` trade that for a Fulcio-issued certificate
  bound to this exact workflow run. No long-lived key ever exists.
- `cosign sign-blob --yes` only accepts Sigstore's non-interactive terms-of-use
  prompt — it does not read or require any repository secret.
- Public repos publish to the public Sigstore transparency log (Rekor) and the
  public Sigstore instance for `actions/attest`; that is intentional and is
  what makes third-party verification (above) possible without trusting us.

## Why cosign is public-repo only

Plain `cosign sign-blob` (unlike `actions/attest`, see below) always talks to
the **public** Sigstore instance, regardless of the calling repository's
visibility. Signing from a non-public checkout would permanently record that
repo's workflow identity (owner, repo name, workflow path, tag) on the public
Rekor log. Cosign keyless signing therefore runs only on this public
`VeyrForge/codehelper` release workflow.

`actions/attest` does not have this problem: for non-public repos it can use
**GitHub's own private Sigstore instance** (no public transparency log). On
public repos, attestations use the public Sigstore instance — which is
intentional and is what makes third-party verification (above) possible.
