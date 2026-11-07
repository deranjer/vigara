# Android mobile client

`apps/mobile` is a native Android (Kotlin, Jetpack Compose) client for a Vigara server. It is separate from the
[Android companion](android.md), which is an enrolled agent that uploads selected files. The mobile client is an
operator console: you sign in with a normal Vigara user and it calls the same `/api` as the web UI. No server
changes are needed.

## Features

| Area | What you can do |
| --- | --- |
| Devices | Health list, host details, current metrics, trend charts (1h to 90d), alerts, backup policies and recent runs, availability schedule (read-only) |
| Containers | Fleet across hosts with search and filters; per-container overview, logs (snapshot or live), live CPU/memory/network stats; start, stop, restart, pause, resume, kill, remove, recreate (with image pull) |
| Stacks | Status and runtime counts, start/stop all, view saved or draft files, revision history, deploy with plan preview, deployment history and reconcile |
| Terminals | Container exec and device shell over the server's WebSocket |
| Docker resources | Images, networks, volumes per host; remove, prune with dry-run preview, pull image |
| Image updates | Update overview and "Check all" |
| Alerts | Incident list |
| Account / admin | Change password, sign out; admins manage users and roles and scheduled tasks |

Roles and Docker tiers match the web UI: buttons that change things are enabled only for operators/admins on hosts with
manage (or deploy for stacks) access.

Also included: interactive terminals (container exec and device shell), stack file editing with draft/save/discard,
encrypted environment reveal and replace, stack creation and import, Git repository settings (including GitHub App
setup in an in-app browser), managed backup setup, snapshot browsing and download, alert rule creation, device
enrollment (saves the one-use installer zip), and MCP connection details and connected-application management.

Limits compared with the web UI:

- The terminal is a line-oriented emulator. Shells, logs, package managers and most CLI tools work; full-screen
  programs (vim, nano, htop) do not render. Typing is sent on Enter, with buttons for Tab, Esc, Ctrl-C/D/L and arrows.
- Stack files are edited as plain text (no Monaco, completion or Compose schema validation). The server still
  validates on save.
- Backup folders are typed one per line instead of browsed on the device.
- Enrollment omits managed-backup, mounted-file and reinstall options; use the web UI for those.
- Stack profiles/placements and reinstalling an existing device's agent remain web-only.

## Connecting

Enter the server address on the sign-in screen. HTTPS is strongly recommended; plain HTTP works for trusted LANs and the
app warns when it is used. If the server sets `VIGIRA_COOKIE_SECURE=true` it only works over HTTPS. Certificates that
chain to a system or user-installed CA are accepted. The session cookie is stored in app-private preferences, excluded
from Android backup and device transfer, and cleared on sign-out or when the server address changes.

## Build

```sh
cd apps/mobile
./gradlew assembleDebug        # debug APK
./gradlew assembleRelease      # unsigned unless RELEASE_STORE_FILE etc. are set
```

Requires JDK 17+ and Android SDK platform 36. The toolchain (Gradle 9.3.1, AGP 9.1.1) mirrors Domitara.

## Releasing through the personal F-Droid repo

1. Add repository secrets to `deranjer/vigara`: `ANDROID_KEYSTORE_BASE64`, `ANDROID_KEYSTORE_PASSWORD`,
   `ANDROID_KEY_ALIAS`, `ANDROID_KEY_PASSWORD`. Keep this keystore forever: F-Droid clients refuse updates signed with a
   different key.
2. Tag and push: `git tag android-v0.1.0 && git push origin android-v0.1.0`. The *Mobile Android Release APK* workflow
   builds `vigara-mobile-0.1.0.apk` (versionCode = major*10000 + minor*100 + patch) and attaches it to a GitHub release.
3. The `deranjer/fdroid` daily workflow downloads the newest release with an APK and regenerates the index. Run it
   manually from the Actions tab to publish immediately. Metadata lives in `metadata/org.vigara.mobile.yml` there.
4. In F-Droid, add `https://deranjer.github.io/fdroid/repo` (already done for Domitara and BookOrbit) and install Vigara.
