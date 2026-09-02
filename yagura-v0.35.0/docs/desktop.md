# Yagura as a desktop app (no terminal required)

Yagura's core is a single local daemon, but you don't need the command line to
use it. The dashboard is an **installable web app (PWA)** — install it once and
it opens in its own desktop window with a Yagura icon, like a native app.

This adds nothing to the core: the daemon, the 62 MCP tools, and the
zero-dependency build are unchanged. The "desktop app" is just the dashboard
the daemon already serves, made installable via web standards.

## 1. Start Yagura without typing commands

- **Windows** — download `yagura-tray.exe` and `yagura.exe`, put them in the
  same folder, and double-click `yagura-tray.exe`. It starts the daemon, shows a
  tray icon (Open / Stop / Quit), and opens the dashboard in an app window.
- **macOS / Linux** — download `yagura-tray` and `yagura`, put them side by side,
  and run `yagura-tray`. It starts the daemon and opens the dashboard as an app
  window (a chromeless Chromium window if one is installed, otherwise your
  default browser). Leave it running; press Ctrl-C to stop.

The launcher needs no GitHub token for the dashboard; scans that hit GitHub need
`YAGURA_GITHUB_TOKEN`, but browsing your portfolio works without it.

## 2. Install the dashboard as a desktop app

With the dashboard open in Chrome, Edge, or any Chromium browser:

1. Look for the **Install** icon in the address bar (or menu → *Install Yagura*).
2. Click it. Yagura now has a desktop/Start-menu icon and opens in its own
   window — no browser tabs, no URL bar.

Behind the scenes the dashboard serves a
[web app manifest](https://developer.mozilla.org/docs/Web/Manifest) at
`/dashboard/manifest.webmanifest` and a small service worker at
`/dashboard/sw.js`, which is what makes it installable and resilient to brief
network blips.

## 3. Add your first project (no terminal)

A fresh dashboard is empty. Use the **+ Add a project** form at the top: enter a
slug (e.g. `breeze`) and a `owner/repo` repository, and click **Register**. The
form sends a `yagura_register` call to the MCP server (the same audited path the
CLI and agents use), then reloads to show your project.

> If this instance was started with `YAGURA_MCP_TOKEN`, the form can't attach the
> token, so it will ask you to register from the CLI instead. The default local
> setup has no token, so the form just works.

## 4. What you can see

The rest of the dashboard is a read-only overview: every project, its stage, CI
status, security signals, staleness, an **Activity** column (recorded agent
tool calls — total · errors · top tool, for any agent that posts to
`/hooks/agent`), and (if configured) agent quota. **Click a project's Activity
count** to open its drill-down (`/dashboard/activity?slug=…`): a structured,
read-only summary of what the agents did — tool- and operation-level counts, the
tool execution sequence, errors, detected anomalies, and which agents took part.
It refreshes on reload. Apart from the register form, the browser never mutates
state directly — and even that goes through the MCP server, so sensor data stays
scanner-only.

## Notes

- The app window points at `http://127.0.0.1:8090/dashboard` (or whatever
  `YAGURA_ADDR` you set). If the daemon is stopped, the window will be blank —
  start it again with the tray/launcher.
- Everything here is local: the dashboard binds to loopback by default and never
  sends your data anywhere.
- Power users keep using the CLI (`yagura list`, `yagura register`, …) and the
  MCP tools; the desktop app is purely additive.
