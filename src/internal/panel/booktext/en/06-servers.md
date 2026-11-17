# Servers of the network

A device network can have several servers. They are all equal: each runs its
own network service `meshd`, and any of them can add the next server or remove
one. A device stays connected to all servers of the network at once.

## How to add a server

You need two servers with the panel: one already works, the other is new.

1. On any server already in the network: **Networks** → the **Servers** tab → **Servers of the
   network** → **Add a server**. The panel shows a code and a **Copy**
   button. The code is one-time and valid for an hour.
2. On the new server: **Networks** → the **Servers** tab → **Servers of the network** → **Join a
   network**. Paste the code and press **Next**.
3. The panel shows what changes on the new server, with numbers. For example:
   "rules replaced here: 4; accounts added here: 6". Press **Join the
   network**.

Nothing else is typed on either side. The network service on the new server is
installed or updated by itself, the progress is in **Settings**. In a minute
the new server appears in the list on all servers of the network.

A third server is added the same way, with a code from either of the two.
Servers find each other by themselves: nothing has to be linked twice.

Servers of one network fully trust each other. Any of them creates device
accounts and changes rules on all of them. Add only your own servers.

## What is shared and what is not

Change it on one server and all get it. Shared:

- access rules: simple mode, groups and rules;
- "New devices — after approval", approvals and freezes;
- names of devices and networks;
- "Remove" of a device and deleting a network;
- device accounts: the SSH user, its keys, freezing, deleting.

Per server: the journal, traffic and limits, server exit through another
server, plugins, the panel password and "Let network devices reach the server
itself" with its ports.

The private key of an account is kept only on the server where it was created.
So "Show settings" works there, and other servers show where the account was
created.

Accounts not created in the panel (for example `root` with a password) are not
passed around. Passwords are not either. A device with such an account does not
move to other servers by itself.

## When this server joins with its own data

The new server takes the access rules, approvals, freezes and names of the
network it joins. Its own are replaced. Accounts are merged: if both have the
same account with different keys, both keys stay.

## How a device learns about a new server

A device gets the list of servers of the network from any server it is
connected to. If its account exists on the new server, the program adds the
profile by itself: same user, same key, device network on. The program shows
one notice "Server … added to the network".

Such a server can be switched off in the program. Then it stays off and does
not come back by itself.

An old version of the program cannot do this and works with one server, as
before.

## One picture on every server

Any server of the network shows the whole network, not only its own devices.
«Overview», «Devices» and «Networks» are the same on every server: the same
devices, the same numbers, the same map. The panels take each other's live
picture every 30 seconds and right away when you open the page.

- **Map.** It shows every server of the network. A line between servers is
  the link between them: green, yellow or red by latency, grey — no link. A
  device has a line to each server it is connected to.
- **Counters.** «Devices» — how many devices are connected to at least one
  server. «Servers» — how many servers the network has and how many are
  reachable now. Latency and traffic come from all servers.
- **Device.** The side panel says «Connected via: Germany · 5 ms, London ·
  30 ms». Below it — which servers the device is not connected to and why:
  old program, old `meshd` on the server, no account, profile turned off, a
  phone adds servers only by hand.
- **Actions.** Rename, approve, freeze, remove and the exit chain work from
  any server, even when the device is connected to another one.

**What grey means.** A server without a link stays on the map with its last
picture: grey, signed «last data — 7 min ago». Its devices are not counted as
online until the link is back. The picture is kept on disk, so the map is not
empty after a panel restart.

If a server runs an old panel, its devices are not visible: it is marked
«old panel there». Update the panel on it.

## Rename a server

**Networks** → **Servers** → the pencil next to any server's name → new name →
**Enter** → confirm. The name is shared: all panels, the map and the devices
see it. The name in the device network (`name.mesh`) changes by the same rules
as before.

## Connection through another server

A device connected only to Germany reaches a device of London and London
itself (`london.mesh` and its allowed ports). The connection goes through both
servers over their link; the map draws it dashed. Access rules are checked on
both servers. Both servers need `meshd` version 7 and a fresh panel; with an
old `meshd` everything works as before, just without this connection.

## Server address for devices

The panel gives the server network an address that can be reached from
outside. A `.mesh` name or a device network address does not work for that.
If the panel runs with `-ssh-host london.mesh`, it says so and gives the
server's public address instead. A server with such an address is marked
«address unusable for devices» in the list.

## Remove a server

**Servers of the network** → **Remove from the network** at the server. On the
server itself the button is **Leave the network**.

The other servers forget it and close its access. The removed server keeps
working alone with its copy of the rules and accounts. Devices stop using it as
soon as they learn the new list: a profile the program added by itself is
switched off with a notice.

To bring a server back, use a new code: **Join a network** on it.

## If one server is gone

Devices stay connected to all servers, so losing one server is usually not
noticeable. Clipboard, files and links between devices go through another
server. Direct links between devices notice nothing.

A server that was offline catches up by itself. Everything changed without it
arrives as soon as it is back. The other servers do not wait for it.

## Moving from the old way

A second server used to be connected through a port forward to the `meshd` of
another server. Such a server shows "This server is connected the old way" and
a **Move** button in **Settings**. In the list of servers on the other side it
is marked "waiting to move".

Press **Move**. The server gets its own network service, its address in the
network stays the same. It joins the network of the server it was connected to
by itself: its key is already allowed there, no code is needed.

Update the panel on both servers first. If the move fails, get a code with
**Add a server** and press **Join a network**.

## What travels with the device

The device keeps its own address and name in the network. They are the same on
all servers:

- **Device name.** The latest change wins.
- **Address in the network.** In a conflict the one who holds the address
  longer wins. If equal, the smaller id wins.

The server's own address in the network differs per server. Do not tie
settings to it.

## Known limitation

The phone can use several servers just like a computer, but it has not been
tested on a real phone yet: treat it as a manual check. How much a second server
adds to battery use is not measured either. More about the phone — in the
program Book.
