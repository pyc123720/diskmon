# Code signing

## Current state

Release binaries are **self-signed** (`CN=diskmon, O=diskmon`, valid to 2036). Windows shows
"Unknown publisher" and SmartScreen may warn on first run. The signature is real (SHA-256 +
RFC 3161 timestamp, verifiable with `signtool verify`), it just is not backed by a public CA.

## Free signing for OSS: SignPath Foundation

> Ready-to-paste application text: [SIGNPATH-APPLICATION.md](SIGNPATH-APPLICATION.md).


[SignPath Foundation](https://signpath.org/) signs open-source Windows binaries for free.
This repository already satisfies the typical requirements:

| Requirement | Status |
|---|---|
| Public repository | yes |
| OSI-approved license | MIT |
| Builds from source in public CI | `.github/workflows/build.yml` |
| No obfuscation / no bundled third-party closed-source blobs | yes (Go stdlib only) |
| Active project, honest description | yes |

### Steps (need a human account)

1. Create an account at <https://signpath.io> and apply to the Foundation program
   (mention the repo URL and that it is a Windows CLI/GUI utility).
2. Install the **SignPath GitHub App** on `pyc123720/diskmon` (only this repo).
3. In SignPath, create a project linked to this repository, then create:
   - an **artifact configuration** (see `signpath/artifact-configuration.xml`),
   - a **signing policy** (e.g. `release-signing`, manual approval),
   - a **GitHub Actions connector**.
4. Copy the identifiers into repository settings:
   - variable `SIGNPATH_ORG_ID`
   - variable `SIGNPATH_PROJECT_SLUG`
   - variable `SIGNPATH_SIGNING_POLICY_SLUG`
   - secret `SIGNPATH_API_TOKEN`
5. Push a tag. `.github/workflows/signpath.yml` is skipped until `SIGNPATH_ORG_ID` is set,
   so `main` stays green meanwhile.
6. After signing, download the signed artifact and attach it to the GitHub Release.

### What SignPath will do

- Sign `diskmon-go.exe` with a publicly trusted certificate (no SmartScreen "unknown publisher").
- Keep the private key in their HSM; the signing step runs in their infrastructure.
- Require a manual approval per release (recommended: keep it).

## Local signing (current workflow)

```powershell
# one-time: create the self-signed cert
$cert = New-SelfSignedCertificate -Type CodeSigningCert -Subject "CN=diskmon, O=diskmon" `
  -KeyUsage DigitalSignature -FriendlyName "diskmon code signing" -CertStoreLocation Cert:\CurrentUser\My
Export-PfxCertificate -Cert $cert -FilePath .\diskmon-codesign.pfx -Password (Read-Host -AsSecureString)

# sign + timestamp
signtool sign /f diskmon-codesign.pfx /p <password> /fd sha256 `
  /tr http://timestamp.digicert.com /td sha256 dist\diskmon-go.exe

# verify
signtool verify /pa /v dist\diskmon-go.exe
```

The `.pfx` and its password are gitignored and must never be committed.
