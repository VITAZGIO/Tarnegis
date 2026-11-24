# Devices

The **Devices** section is the list of everyone who may connect to the server.
Each device has its own key and its own system account on the server.

## Add a device

1. Press **+ Add device**.
2. Enter the **Device name** ("Laptop", "Phone") and choose the type: Windows,
   Linux or Android.
3. The panel creates the device and shows the ready settings.

Right after creation there is a QR code and a **Copy link** button (the
`sshtunnel://` link). They are shown once, so save them straight away.

Later the device keeps a **Show settings** button. It holds the settings file
and the fields: address, port, user and key. There is no QR code there.

There are three ways to pass the settings on:

- on a phone, scan the QR code;
- on a computer, paste the link with the **Paste** button in the program;
- download the file and open it with the **Import** button.

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
- **Delete** — erases the account and the key. Without new settings the device
  will not log in again. The panel asks for confirmation.

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
