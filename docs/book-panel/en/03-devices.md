# Devices

The **Devices** section is the list of everyone who may connect to the server.
Each device has its own key and its own system account on the server.

## Add a device

1. Press **+ Add device**.
2. Enter the **Device name** ("Laptop", "Phone") and choose the type: Windows,
   Linux or Android.
3. Tick the **Servers** the device may use. This server is always ticked: the
   device joins through it. Later you can untick it like any other.
4. **Device network** is on by default. Turn it off if the device only needs
   internet access through the server.
5. Press **Create invite**.

The panel shows an **invite** — one line starting with `sshtun1:` and its QR
code. Paste the line into the program on the device (a phone can scan the
code). The invite works for 24 hours and only once; the panel does not show it
again. The card also says which server the device will join through: always
the server whose panel created it, even if that panel is open only locally.

The program makes its own key: the private key never leaves the device and the
panel does not know it. From then on the panel decides which servers the device
may use and whether it has the device network, and the program follows by
itself.

Unused invites are listed under the device list: for whom, when issued and
until when they work. Drop one with **Revoke**. At most 20 invites can be open.

Devices created before invites still have the **Show settings** button (the
settings file with the key). New ones do not: the panel has no key for them.

## A device's servers and network

The **Servers and network** button in the device card:

- **Servers** — which servers of the network the device may log in to. On a
  server without the tick the device's account does not exist at all — no user,
  no key. Untick a server and the account leaves it at the next sync between
  servers; tick it and the account appears.
- **Device network** — when off, the device cannot join the device network on
  any server (the server itself refuses, not just the program).

The change can be made on the panel of any server of the network — it reaches
every server and the program on the device by itself. Devices that are not
allowed on this server are in the list too: their card says there is no
account here, and the same button lets them in.

Devices created earlier have all servers ticked and the device network on — as
before.

**New invite** — when the device lost its key (the program was reinstalled, a
new phone). It adds one more key to the same account; old keys stay until you
remove them in **Servers and network** → **Keys**.

## The list: address, name, system

Two values in a device row are copied with a click: the **network address**
(the **Address** column, for example `198.19.0.4`) and the **network name** under
the device name (for example `noutbuk.mesh`). Each click copies only its own
value. "Copied" appears in the same row, in the empty space between **Address**
and **System**, and moves nothing. A click on the rest of the row opens the
device card.

The system has an icon in front of it: Windows, Android or Linux. An unknown
system has no icon.

## Freeze, disconnect, delete

Each device has three actions. They differ in strength:

- **Freeze** — temporarily forbids SSH login. The key stays on the server, so
  **Unfreeze** gives access back without a new setup.
- **Disconnect** — ends the current sessions but does not touch the key. The
  device can connect again.
- **Delete** — erases the account and its keys on every server of the network
  (servers that are offline now — at their next sync). Without a new invite the
  device will not log in again. The panel asks for confirmation.

## Why an SSH login and a network device are one row

One device can go to two places: by SSH to the server and into the device
network. The panel joins them into one row if the SSH user name matches the user
of the network device. That is why **Freeze** on the device row and **Freeze in
network** are different things.

## Approving new devices

If **New devices — after approval** is on in **Networks**, a new device of the
network waits first. In the program on it, the text says it is waiting for
approval in the server panel. Other devices do not see it.

The device card gets these buttons:

- **Approve** — the device joins the network and the others see it;
- **Reject** — the device disappears. If it tries again, it waits for approval
  again.

Approval works only on this server. If the device has a profile of another
network server, approve it there separately.

## Device auto-update

The device card shows the app version and build (VPN or proxy), when it was last
connected and the **Receive auto-updates** checkbox (on by default). On — the
device updates itself to the network version once the servers are updated. The
device learns about the change when it connects; the latest change wins, the
panel's or the person's on the device. Details are in the "Update" chapter.

## Freeze in network

The **Freeze in network** button is different. The device stays in the network
with its address and name. But it connects to nobody, and nobody connects to it.
Direct connections to it are cut. SSH login is not touched by this button, so
the device can still log in to the server.

In short: **Freeze** forbids login to the server, and **Freeze in network** cuts
the device off from the network. Connection comes back with **Unfreeze**.

As with approval, freezing works only on this server. If the device is online
through another network server too, freeze it there separately.
