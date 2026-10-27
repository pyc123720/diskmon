# SignPath Foundation application (ready to paste)

Use this page when filling in the SignPath Foundation application form. Replace
`<your email>` with the address you want SignPath to contact.

## Short description (form field: "What does your project do?")

diskmon is a small, open-source Windows disk monitor written in Go. It answers three questions:
which folders consume the C: drive, what is growing right now (attribution from the NTFS USN
journal, down to file and process), and what can be removed safely. Every cleanup is a move to a
quarantine folder that can be undone for 7 days; permanent deletion is only possible through an
explicitly enabled, dry-run-first rule for safe-tier folders. The application ships as a single
~9 MB executable with a tray icon and a localhost-only web UI. It uses the Go standard library
only, has no telemetry, and never sends data anywhere.

## Longer text (form field: "Anything else we should know?")

- Repository: https://github.com/pyc123720/diskmon
- License: MIT (OSI-approved)
- Releases: https://github.com/pyc123720/diskmon/releases
- CI: GitHub Actions, `.github/workflows/build.yml` (windows-latest): `go vet .`, `go test ./...`,
  then builds the executable from source on every push.
- Dependencies: Go standard library and `syscall` only; `go.mod` has no requirements.
- Build command (reproducible, no network needed after checkout):

  ```powershell
  go build -trimpath -ldflags="-H windowsgui -s -w -X main.version=<tag>" -o dist/diskmon-go.exe .
  ```

- Artifact: one PE32+ executable, Windows GUI subsystem, x64. No installer, no services, no
  drivers, no kernel components.
- Why the signature matters: users download a utility that inspects and moves files on their system
  drive. SmartScreen currently shows "Unknown publisher", which is a real adoption barrier for a
  tool that asks for trust around deleting data.
- Safety model: monitoring is read-only; cleanup moves files into `<data-dir>\quarantine\...` with
  an index that supports restore and rebuild; a deny-list covers system folders and a whitelist
  protects user documents by default; rules default to dry-run.
- Privacy: no accounts, no telemetry, no network calls except an optional loopback HTTP server on
  127.0.0.1 for the local UI; the app works offline.
- Project age: created 2026-09; actively developed (22 commits in the first weeks, 27 Go tests).
- Maintainer: GitHub user `pyc123720`, contact `<your email>`.

## Typical form answers

| Field | Value |
|---|---|
| Project name | diskmon |
| Project website | https://github.com/pyc123720/diskmon |
| Repository URL | https://github.com/pyc123720/diskmon |
| Download URL | https://github.com/pyc123720/diskmon/releases/latest |
| License | MIT |
| License URL | https://github.com/pyc123720/diskmon/blob/main/LICENSE |
| Programming language | Go 1.27 |
| Build system | Go toolchain + GitHub Actions |
| Artifact type | Windows executable (.exe), signed with Authenticode |
| Signing policy requested | release signing, manual approval per release |
| Version scheme | semantic tags `vMAJOR.MINOR.PATCH` |

## After approval

1. Put the identifiers in repository variables:
   `SIGNPATH_ORG_ID`, `SIGNPATH_PROJECT_SLUG`, `SIGNPATH_SIGNING_POLICY_SLUG`,
   and the secret `SIGNPATH_API_TOKEN`.
2. Push a tag: `.github/workflows/signpath.yml` starts signing and uploads `diskmon-signed`.
3. Download the signed executable from the SignPath artifact / GitHub artifact and attach it to the
   matching GitHub Release.
