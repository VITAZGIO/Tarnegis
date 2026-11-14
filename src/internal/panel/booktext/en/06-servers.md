# Network servers

A device network can have several servers. There is no main one: each server is
on its own, with its own `meshd` (the network service on the server). A device
keeps a connection to every server whose profile it has. All of them use one
network key.

## How to add a server

A server joins the network when the device has its profile with the same network
key and the device network is switched on. The panel helps you find such servers:

1. Open **Networks** → **Servers of the network**.
2. In the block **Suggest to devices**, enter the **Name**, the **Server address**
   and the **Port**, and press **Add server**.

The devices of this server learn from the list which other servers the network
has. In the program window on the device such servers are shown in the list of
servers suggested by panels, with an **Add profile** button. The address and the network key are filled in
automatically. Enter the user and the key of the server yourself.

## Moving from the old way

In the past a second server was connected as a "secondary" one: through a port
forward to the `meshd` of the main server. Such a server is marked in the panel
as the old way. To move to its own network service, open **Settings** → **Switch
to own network service** on this server.

The order is:

1. Update the program on the devices: after the switch an old version talks
   only to the server of its own profile.
2. Update `meshd` on the first server (see the chapter "Update").
3. For each device, add both profiles with one network key. The device will see
   both servers.
4. On the second server, press **Switch to own network service**. The old
   forwarding process stops, and the address in the network stays the same.
5. Set the same access rules in both panels.

## If one server dies

Devices keep a connection to every server, so losing one server is usually not
noticed. The clipboard, files and the connection between devices go through the
second server, which both devices share. Direct connections between devices do
not notice anything.

If there is no shared live server, the program on the device says "no shared
server: it is online through …". This happens when two devices have profiles of
different servers, and no server is reachable by both.

## What is separate on each server

These things are separate on each server:

- access rules;
- the journal;
- approval of new devices;
- freezing of devices "in the network";
- removing a device and deleting a network.

If you change one of them, the other servers do not learn about it. So all
servers must have the same rules. Otherwise the panel writes "rules differ".

## What travels with the device

The device keeps its own address and name in the network. They are the same on
all the servers that see it:

- **Device name.** If you rename it on one server, it changes on the others too.
  The latest change wins.
- **Address in the network.** On a conflict, the one that has held the address
  longer wins. If that is equal, the smaller identifier wins.

The address of each server in the network is its own. Do not tie settings to it.

## Known limit

A phone can use several servers the same way a computer does, but this has
not been run on a real phone yet: treat it as a manual check. How much battery
a second server costs has not been measured either. For the phone in detail,
see the Book of the program.
