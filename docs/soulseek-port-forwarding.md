# Soulseek: opening the listening port

Soulseek works without it, but with the listening port open to the internet many more users
can reach you: people whose own port is closed can only exchange files (and search answers)
with users whose port is open. Universal Music Downloader listens on **TCP port 2234** by
default (Sources → Soulseek → Enter → *Listening port*; if 2234 is taken it uses the next free
one, and the Soulseek tab shows which).

The steps:

1. [Route Soulseek past your VPN](#1-route-soulseek-past-your-vpn) (only if you use one).
2. Give your computer a fixed address on your network.
3. Forward the port on your router: [MikroTik](#mikrotik-routeros-7), or
   [any other router](#other-routers), or let the app do it with [UPnP](#upnp-the-app-opens-the-port-itself).
4. [Check it](#4-check-it).

## 1. Route Soulseek past your VPN

A forwarded port is on your *home* internet address. Through a VPN, the Soulseek server sees the
VPN's address instead, and tells other users to connect there, where nothing is forwarded.

In the app: **Sources → Soulseek → Enter → Route: direct**, then **Save**. Soulseek's
connections then go through your network card and router, bypassing the VPN (done inside the
app, per connection; your system's routes and the VPN aren't changed). The app also binds its
listening socket to the network card, so the answers to incoming connections go back through
your router rather than into the VPN's tunnel.

Without a VPN, leave the route on *system*.

## 2. A fixed address for your computer

Port forwarding sends the port to one address on your home network, so that address must not
change. Find your computer's address (Linux: `ip -br addr`; Windows: `ipconfig`), e.g.
`192.168.1.50`, and make it permanent on the router (a *static DHCP lease*, below).

## 3. Forward the port

### MikroTik (RouterOS 7)

In **WinBox** or **WebFig** (the router's web page):

1. **IP → DHCP Server → Leases**: find your computer (by its address or name) → open it →
   **Make Static** → OK. Its address stays the same from now on.
2. **IP → Firewall → NAT → +** (add):
   - *General*: **Chain** `dstnat`, **Protocol** `6 (tcp)`, **Dst. Port** `2234`,
     **In. Interface List** `WAN` (or **In. Interface** your internet port: `ether1`, or
     `pppoe-out1` if your provider uses PPPoE).
   - *Action*: **Action** `dst-nat`, **To Addresses** your computer's address
     (`192.168.1.50`), **To Ports** `2234`.
   - *Comment*: `Soulseek`. **OK**.
3. **IP → Firewall → Filter Rules**: nothing to add with MikroTik's default configuration: its
   rule *"drop all from WAN not DSTNATed"* lets forwarded connections through. If you have your
   own rules that drop new connections from WAN, add an accept rule for them above those
   (below).

The same from the router's **Terminal** (WinBox → New Terminal, or SSH):

```
/ip dhcp-server lease make-static [find address=192.168.1.50]
/ip firewall nat add chain=dstnat in-interface-list=WAN protocol=tcp dst-port=2234 \
    action=dst-nat to-addresses=192.168.1.50 to-ports=2234 comment="Soulseek"
```

Only if your own filter rules block it:

```
/ip firewall filter add chain=forward in-interface-list=WAN protocol=tcp \
    dst-address=192.168.1.50 dst-port=2234 action=accept comment="Soulseek" place-before=0
```

(`place-before=0` puts it first; move it under your *established, related* rule if you like.)
Replace `192.168.1.50` with your computer's address, and `2234` if you changed the port in
the app.

To undo it: **IP → Firewall → NAT**, select the *Soulseek* rule, **–** (or
`/ip firewall nat remove [find comment="Soulseek"]`).

### Other routers

The menu is usually called *Port forwarding*, *Virtual server*, *NAT* or *Applications*: forward
**TCP** port **2234** (external) to port **2234** of your computer's address, and give the
computer a fixed address (*DHCP reservation*, *static lease*, *address reservation*). Your
router's manual or https://portforward.com has the steps for most models.

### UPnP: the app opens the port itself

With **UPnP** on in the router, the app asks it to forward the port while the app runs (Sources
→ Soulseek → Enter → *Ask the router to forward the port (UPnP)*, on by default, as in
Nicotine+), and takes the forwarding away when it quits. No manual rule is needed then.

On MikroTik: **IP → UPnP** → *Enabled*; **Interfaces → +**: your internet port (`ether1` or
`pppoe-out1`) as *external*, your LAN `bridge` as *internal*. In the terminal:

```
/ip upnp set enabled=yes
/ip upnp interfaces add interface=ether1 type=external
/ip upnp interfaces add interface=bridge type=internal
```

UPnP lets any program on your network open ports; the manual rule above opens only this one.

### Your computer's firewall

If your computer runs a firewall, let TCP port 2234 in: `sudo ufw allow 2234/tcp` (ufw), or
`sudo firewall-cmd --permanent --add-port=2234/tcp && sudo firewall-cmd --reload` (firewalld).
On Windows, allow the app when Windows Defender Firewall asks the first time.

## 4. Check it

In the **Soulseek** tab press **o**: the app asks the Soulseek site's port checker whether the
port can be reached from the internet at the address Soulseek sees. "open" means it works.
(Trying it yourself from inside your network often fails even when it works, because many
routers don't route their own public address back in.)

If it stays closed:

- **The route**: with a VPN on, Soulseek must use the *direct* route (step 1).
- **Carrier-grade NAT**: compare the internet address on your router (MikroTik: **IP →
  Addresses**, the one on your internet port, or `/ip address print`) with the address the
  Soulseek tab shows. If the router's is in `100.64.0.0`–`100.127.255.255`, or a private range
  like `10.x` or `192.168.x`, your provider shares one public address among several customers,
  and no forwarding on your side can open a port. Ask your provider for a public (*white*)
  address, or use a VPN that offers port forwarding and leave Soulseek's route on *system*.
- **The address changed**: if the computer got a new address after a restart, the static lease
  in step 2 is missing.
