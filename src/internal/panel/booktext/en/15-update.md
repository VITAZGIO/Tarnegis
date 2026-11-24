# Update

Three things are updated: the network service `meshd` on each server, the plugin
host and the panel itself. The program on the devices is updated last.

## Order

Servers first, then devices:

1. **`meshd` on each server.** The **Update meshd** button in **Settings**. Version 5
   is needed for approval of new devices and for freezing in the network, version 6 —
   for devices to add new servers of the network by themselves.
2. **Plugin host.** If the plugins are installed, press **Update** in the host card.
3. **The program on the devices.** Update it after the servers.

If there are several servers in one server network, press **Update on all
servers** (see below) — every server updates its panel and `meshd` by itself, in
turn. Otherwise update them one by one.

Why `meshd` comes first: approval of new devices and freezing in the network work
only with version 5 of the service. An old service does not understand them.

## The panel

**Settings** → the **Panel** tab → block **Panel version** → **Check for updates**. If a new version is
out, the button **Update to …** appears.

The panel downloads its own file from the release on GitHub. It checks the file
against the checksum, replaces the file and restarts. It takes about a minute. The
page refreshes itself, and you do not need to log in again. Connected devices are
not affected.

If the server has no internet access, the update check does not work. The panel
writes that it cannot reach GitHub.

**Update on all servers** — the same, for the whole server network: every server
downloads and installs the new version itself (the panel, then `meshd`), the
server where you pressed it goes last. Details are in the «Servers of the network» chapter.

## Network devices

**Settings** → **Panel** → the **Network devices** block. It shows the network
version and every device of the device network: its app version, system and
build (VPN or proxy) and its state: **up to date**, **behind**, **offline** (it
updates when it connects) or **auto-update off**.

The network version is this panel's version or the one **Update on all
servers** was pressed for, whichever is newer. The server tells it to every
device when it connects. A device that is older and has auto-update on updates
itself: it downloads its own file from the GitHub release, checks it against the
checksum and restarts. The network passes only the version number; every device
fetches the file itself. Nothing goes down. A broken release is retried by a
device at most once an hour.

Devices that are behind and connected to this server have an **Update** button;
**Update all devices** does it for all of them. The button works even with
auto-update off. A failure on one device shows in its row and does not stop the
others.

The device card (**Devices**) has a **Receive auto-updates** checkbox. The person
sees the same in the app: **Settings** → **Advanced** → **Auto-update**. The
latest change wins, the panel's or the device's.

The whole network can also be updated from a device with the **My device** role:
in the app, **Check for updates** asks "Update the network to vX?" when GitHub has
a version newer than the servers. The server accepts only the number of the
latest official release that is newer than the network version, and presses
**Update on all servers** itself. Guests and servers are not asked.

A phone cannot install itself silently: it downloads the update and shows the
notification "Update vX is ready — install".

An old app on a device does not understand the network version — update it by
hand. An old `meshd` (before version 8) does not pass the network version to
devices: the block says that `meshd` needs an update.

## meshd

**Settings** → **Device network** → **Update meshd**. The meshd version is shown
there too: "7 · v1.10.6" — the protocol number and the release meshd was built
from. A hand-built one shows "7 · file from …". If it is the same release as the
panel, the button answers "meshd is already the latest: … Nothing to install".

The install progress is shown in the same **Network service** card, under its
buttons; the same goes for **Install UDP relay** and **Install plugin host**. The other cards do not move. The log stays until the page is reloaded or
until the **×** in its corner.

While meshd restarts, the device network on this server is gone for a few
seconds. If the panel is open through the device network of this same server, the
page reconnects by itself.

## Versions before 1.7.3

Old versions of the panel have no update button. They are updated once with a
command run as root. The command is written in the panel installation instructions
in the repository on GitHub. After that you can update from the settings.

## Reset the password

If the password is lost, reset the account. Delete the file `users.json` and restart
the service. More in the chapter "First login".

## Change the panel port

The panel port is set with the `-listen` flag. The default is `127.0.0.1:47823`.
To change the port, edit the `ExecStart` line in the service and restart it:

```bash
sudo sed -i 's|^ExecStart=.*|ExecStart=/usr/local/bin/ssh_tunnel_panel -listen 127.0.0.1:8443|' \
  /etc/systemd/system/ssh_tunnel_panel.service
sudo systemctl daemon-reload
sudo systemctl restart ssh_tunnel_panel
```

With the `-domain` flag the port does not matter: the panel listens on `:80` and
`:443`.

## Check after an update

Do this after each update:

1. **The plugin host is running.** If the plugins are installed, check the service
   with `systemctl status ssh_tunnel_node`. If the service does not start, send the
   output of `journalctl -u ssh_tunnel_node -n 50`.
2. **The plugin ports are open.** Open "What blocks the plugins" and press **Open the
   plugin ports** if it says something.
3. **The devices see each other.** Open **Networks** and check that each device is
   online.
4. **The programs on the devices are updated.** They are updated after the servers,
   see "Order" above.
