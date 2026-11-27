# Remote desktop

A Windows desktop over RDP from a PC or a phone, without port forwarding.

## RDP relay — install once

RDP itself is handled by a **relay** — guacd (Apache Guacamole) on a computer
in the **same local network as the machines**:

    docker run -d --name guacd --restart unless-stopped -p 4822:4822 guacamole/guacd

Enter its address at the top of the plugin: name.mesh:4822 via the device
network (works from anywhere) or 192.168.1.x:4822 locally. The relay settings
and «Add a device» are folded into one line each: tap to open, tap again to
close.

## Machines and session

Add a machine by its home network address. The login can be remembered on
this device. Inside «Add a device», the folded **«Wake-on-LAN»** takes the
network card MAC and, optionally, a broadcast address and port: the machine
gets «Wake». All your devices see the entry and all of them send the packet,
so a device next to the PC (say, a home server) wakes it from anywhere. In a session: on-screen keyboard (button toggles it), LMB hold / RMB, 🖼 paste a phone photo into the PC (needs Phone link there), touchpad/touch
modes, sticky Ctrl/Alt/Shift/Win, Ctrl+Alt+Del, Alt+Tab, quality, scale (auto
or 100–300 %, remembered per machine on this device), full screen. The
resolution follows the window; with the keyboard open the picture keeps the
full phone width and the visible strip follows the cursor. A dropped
connection reconnects itself.
