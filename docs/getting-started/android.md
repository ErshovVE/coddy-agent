# Android (Termux)

Coddy runs on Android phones and tablets inside [Termux](https://termux.dev) from a release built
for Android, **`coddy_X.Y.Z_android_arm64.tar.gz`**. It is the whole Coddy: the console,
**`coddy serve`** with the web UI, the Telegram bot and **`coddy acp`**. The build is for 64-bit ARM
(aarch64), the processor of current Android devices.

## Install

The install script recognises Termux and fetches the Android build:

```bash
pkg install curl
curl -fsSL https://coddy.dev/install.sh | bash
```

It installs **`coddy`** into **`~/.local/bin`**, the man page and the completions into
**`~/.local/share`**, and writes the block that puts them on **`PATH`** into **`~/.bashrc`**, which
Termux's login shell reads. Running the script again replaces a binary it installed earlier, the
Linux build a previous attempt left there included. Open a new session, or reload the file, and
check:

```bash
source ~/.bashrc
coddy -v
```

By hand, with **`X.Y.Z`** replaced by the release you want:

```bash
curl -fsSLO https://github.com/coddy-project/coddy-agent/releases/download/X.Y.Z/coddy_X.Y.Z_android_arm64.tar.gz
tar -xzf coddy_X.Y.Z_android_arm64.tar.gz
install -m 0755 coddy "$PREFIX/bin/coddy"
```

**`$PREFIX/bin`** is on Termux's **`PATH`** already. Keep the binary in Termux's own storage, as
both examples do: Android mounts the shared storage (**`/sdcard`**, **`~/storage`**) without the
right to execute anything from it.

The first run needs a provider key in **`~/.coddy/config.yaml`**, as on any other system; see
[Configuration](configuration.md).

## Why the Linux archive does not run

The **`linux_arm64`** archive holds a static Linux executable, and Termux cannot start it everywhere.
Android 10 and later refuse an app that targets them the right to execute files from its own data
directory, so a Termux that targets them, such as the Google Play build, starts every program
through Android's linker, **`/system/bin/linker64 <program>`**. The linker loads position-independent
executables only and turns the static one away:

```text
error: "/data/data/com.termux/files/home/.local/bin/coddy" has unexpected e_type: 2
```

Where Termux executes files directly (the F-Droid and GitHub builds), the Linux binary starts, but
the Linux paths it relies on are missing: it finds no CA certificates and no **`/etc/resolv.conf`**,
so every request to a provider fails, and on the Android versions whose seccomp filter rejects the
**`faccessat2`** system call it is killed with **`Bad system call`** the first time it looks a
program up.

The Android archive is built with **`GOOS=android`**: a position-independent executable that names
**`/system/bin/linker64`** as its interpreter and needs no shared library, which both kinds of Termux
start. The rest of this page is what that build does differently at run time.

## What Coddy adapts on Android

Nothing needs configuring: Coddy reads the Termux environment when it starts.

- **Programs it starts.** **`run_command`**, background tasks, hooks, MCP servers over stdio, git and
  ripgrep are started the way Termux starts its own programs. Where Termux goes through
  **`/system/bin/linker64`**, Coddy does too, and a script is handed to its interpreter; a script
  whose first line names **`/usr/bin/env`** or **`/bin/sh`** gets the Termux program of that name.
  This is what the **`termux-exec`** library does for Termux's own programs; Coddy, like any Go
  program, starts processes with a system call the library cannot see, so it does the same itself.
  A shell command runs in Termux's **`bash`** and behaves as it does in your session.
- **Its own path.** Started through the linker, Coddy finds its binary from the command line rather
  than **`/proc/self/exe`**, which names the linker, so **`coddy update`** and
  **`coddy serve --daemon`** reach the right file.
- **Name resolution.** Android has no **`/etc/resolv.conf`**, and without it the resolver Coddy is
  built with asks **`127.0.0.1:53`**, where nothing listens. Coddy asks the nameservers of
  **`$PREFIX/etc/resolv.conf`** instead, the file Termux's own tools read, which ships with Google
  Public DNS (**`8.8.8.8`**, **`8.8.4.4`**). Edit that file to use others and restart Coddy. The
  device's Private DNS setting and the DNS of a VPN app are not consulted.
- **Certificates.** Coddy trusts the bundle of Termux's **`ca-certificates`** package,
  **`$PREFIX/etc/tls/cert.pem`**, next to Android's system store. It does so through
  **`SSL_CERT_FILE`**, so a bundle you export yourself wins.
- **Temporary files.** **`TMPDIR`** falls back to **`$PREFIX/tmp`**: the Android default,
  **`/data/local/tmp`**, is not writable by an app.
- **Opening a browser.** Sign-in flows such as **`coddy providers login`** open the phone's browser
  with **`termux-open-url`**, and the browser reaches the callback Coddy listens for on the device's
  loopback address.
- **Updates.** **`coddy update`** downloads the Android archive of the new release and replaces the
  binary in place; see [Update](update.md).

## Running `coddy serve`

**`coddy serve`** works as elsewhere, and the web UI opens at **`http://127.0.0.1:12345/`** in the
phone's browser. Android has no systemd, so **`coddy serve install`** is not available: run the
server in a Termux session, in **`tmux`** (**`pkg install tmux`**), or in the background with
**`coddy serve --daemon`** (see [coddy serve](../operate/serve.md)).

Android stops the processes of an app it considers idle. While the server has to stay up, take
Termux's wake lock (**`termux-wake-lock`**, or from the Termux notification) and let Termux run
unrestricted in the battery settings. Android 12 and later also kill the background processes of an
app beyond a limit (the "phantom process killer"); Termux's documentation describes how to lift it.

## Limitations

- Only 64-bit ARM is published. Go links an Android build for any other architecture only with the
  Android NDK, which the release does not use, so 32-bit ARM and x86 Android devices are not covered.
- Coddy resolves hostnames with the nameservers of **`$PREFIX/etc/resolv.conf`**, not with the
  resolver Android configures for the network (see above).
- A program that is itself a static executable cannot run where Termux starts programs through the
  linker, whoever starts it; that is Android's rule, and the error is the same **`unexpected e_type`**.

## Building it yourself

**`make android`** cross-compiles **`build/coddy-android-arm64`** on any machine with Go, with
**`TAGS`** as for **`make build`**, and **`make check-android`** type-checks the Android build; see
[Build from source](../contributing/build.md#android-termux).
