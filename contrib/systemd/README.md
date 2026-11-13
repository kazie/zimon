# systemd units for zimon

Two jobs:

- **`zimon-update.timer`** runs `zimon update --yes` once a week. Downloads
  resume, so a reboot in the middle is fine. `Persistent=true` catches up on a
  missed run.
- **`zimon-serve.service`** runs `zimon serve`. It writes `library.xml` and
  runs `kiwix-serve` behind its start page; kiwix-serve reloads the library
  by itself when zimon adds, updates or removes a ZIM. You never need to
  restart it.

Pick **user** units on a desktop, or **system** units on a server/NAS.

## User units (desktop, or a NAS where you have a login)

They assume zimon is at `~/go/bin/zimon` (from `go install`). Edit `ExecStart`
if yours is elsewhere. `zimon serve` only answers this machine unless you set
`serve.address = "all"` in your config.

```sh
mkdir -p ~/.config/systemd/user
cp contrib/systemd/user/* ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now zimon-update.timer
systemctl --user enable --now zimon-serve.service   # optional
# Keep them running while you are logged out:
loginctl enable-linger "$USER"
```

## System units (headless server)

They run as a `zimon` user with the config at `/etc/zimon/config.toml`, ZIMs
in `/srv/zim` and state in `/var/lib/zimon`. What the server should hold is in
`/etc/zimon/library.toml`; `/etc/zimon` belongs to the `zimon` user so
`zimon add`, `level` and `remove` can update it (the services only read it).

```sh
sudo useradd --system --home-dir /var/lib/zimon --create-home zimon
sudo install -d -o zimon -g zimon /srv/zim /etc/zimon
sudo install -m 755 zimon /usr/local/bin/zimon
printf '%s\n' 'library_dir = "/srv/zim"' 'state_dir = "/var/lib/zimon"' 'languages = ["eng"]' \
  '[serve]' 'address = "all"' | sudo tee /etc/zimon/config.toml
sudo cp contrib/systemd/system/* /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now zimon-update.timer zimon-serve.service
```

Add collections or single ZIMs as the `zimon` user:

```sh
sudo -u zimon ZIMON_CONFIG=/etc/zimon/config.toml zimon add @linux @repair --level standard
sudo -u zimon ZIMON_CONFIG=/etc/zimon/config.toml zimon add wikipedia_en_all:nopic
```

Keep `/etc/zimon/library.toml` in git if you like. After editing it by hand,
run `zimon apply` (same `sudo -u zimon ...` prefix) to download and delete
what changed; the weekly timer only fetches new releases and, within your
budget, new collection items.

## Watching it

```sh
systemctl list-timers 'zimon*'
journalctl -u zimon-update -f      # --user -u ... for user units
```

Without a terminal, progress is logged as one line per 10% (or every 30
seconds), so the journal stays readable.
