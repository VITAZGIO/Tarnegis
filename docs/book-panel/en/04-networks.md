# Networks

The **Networks** section shows the device networks that the server knows about,
and the servers of those networks.

## What a network and a network key are

A **device network** is a set of devices that see each other by permanent
addresses. The devices are joined by a **network key**. The key is created when
the device network is switched on on the first device. The other devices get it
by export from the first. The program describes this in its own Book, in the chapter
"Device network".

While two devices have the same key, they are in the same network. A different
key means a different network, even if the addresses match.

## Panel network

Devices added **by invite** (chapter "Devices") do not create a network key:
the panel hands it out. The panel — all servers of the network together — has
one network of its own, the **panel network**. In the list of networks it is
marked **panel network**: every device added by invite lands there, whichever
server invited it.

The panel network is created by itself with the first invite that has the
device network on. It can also be created in advance with **Create panel
network** in **Networks**. While it has no devices yet, a note stands in place
of its network card.

The panel network key is kept on the servers and never shown: not in the panel,
not in the invite, not in the journal. A device gets it through its own login,
and only if the device network is allowed for it and it is not frozen.

Older networks stay as they are. To move an older device into the panel
network, use **New invite** on its page in **Devices**. If a server is old and
does not hand out the key, the program on the device says "the panel did not
hand out the device network — update the server" and keeps its device network
off.

## Network name

A network can be named. The name is shown on the network map and in the server
list. It is handy to name it after a place: "Home", "Office".

1. Press **Rename network**.
2. Enter the **network name** and save.

If the name is cleared, the network is called by its number again ("Network N").

## Delete a network

The **Delete network** button erases the network on this server. All its devices
on this server are disconnected.

**Important: a deleted network can come back.** The network is taken from its
key. As soon as any device connects to the server again with its key, the network
appears — empty and without a name. To keep it from coming back, first remove
this network's key in the program on each device, or delete those devices in the
**Devices** section. Then the network stays deleted until nobody connects with
that key.

An empty network with nobody in it is removed by itself 7 days after it became
empty. If a device appears in it again, the count starts over.

## New devices — after approval

The **New devices — after approval** switch turns on the check. A new device that
connects for the first time waits in the **Devices** section until it is
approved. Devices that are already known are not affected by the check.

The switch works only on this server. More in the chapter "Devices".

## What the network card shows

Each network in the list shows:

- **Created** — when the network appeared on the server;
- how many of its devices are **online** now, out of the total;
- its **ID** — a service number of the network;
- **Removed automatically** — if nobody is in the network, here it says when it
  will be removed.

If the network has no devices, the panel says that it is empty. Before you delete
an empty network, make sure that its key is no longer needed by anyone.

## Plugin data of a deleted network

If a network was deleted but its plugin data (folders, clipboard, backups)
remains, the network card says "Network deleted, data left". The server never
erases this data by itself. You can erase it with the **Delete data** button. This
cannot be undone. More in the chapter "Server plugins".
