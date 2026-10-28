# IPv6 and dual-stack guest networking

Status: design. Steps 1-4 of [section 9](#9-implementation-plan) are
done: the guest kernel (section 1.1), v6 discovery, the v6 splice guard
(section 4), and the guest-side configuration (sections 3.2-3.4 and 6).
IPv6 DNS and end-to-end coverage aren't implemented yet, and are split
into the follow-up tasks listed there.

## 1. Where things stand

netshim gives the guest the sandbox's network identity, and today that
identity is IPv4 only, at every step:

| Step | Code | IPv4-only behaviour |
|---|---|---|
| Find the interface | `pickExternalIface` (pkg/netshim/iface.go) | Looks only at IPv4 default routes and IPv4 addresses. A v6-only pod fails with "no IPv4 default route and no non-loopback interface with an IPv4 address". |
| Read the identity | `DiscoverIdentity` (pkg/netshim/guest.go) | Reads `FAMILY_V4` addresses and routes only. Fails with "no IPv4 address found on <iface>". |
| Carry it to the guest | `GuestConfig`, `WithGuestParams` | The kernel `ip=` parameter, which is IPv4-only, plus `granule.routes=`, `granule.mtu=` and `granule.dns_*=`. `carriedRoutes`/`normalizeDst` drop anything that isn't IPv4. |
| Apply it in the guest | in-kernel `CONFIG_IP_PNP`, or `granule-configure-net` (klibc `ipconfig`, Debian with a distro kernel), then `granule-configure-routes` and `granule-configure-dns` | All IPv4. The route script's prune pass uses `ip -4 route show`. |
| Guard the splice | `spliceGuest` / `addGuardFilters` (pkg/netshim/splice.go) | tap → ext passes only IPv4 and ARP from the guest's MAC and address. IPv6 (0x86DD), VLAN tags and every other ethertype are dropped. |
| Resolver | `ParseDNS`, `GuestResolvers`, `FallbackDNS` (pkg/netshim/dns.go, config.go) | IPv4 nameservers only, because they travel in `ip=`'s dns0/dns1 fields. The fallback is `8.8.8.8`. |
| Control link | `FromEnv`, `guardControlLink` | `169.254.100.1/24` on `granule0`. `NETSHIM_CONTROL_CIDR` must be IPv4, and the control tap drops IPv6. |

What that means in practice:

* **IPv6-only Kubernetes** (kind `ipFamily: ipv6`, EKS IPv6 clusters, and
  IPv6-only GKE where it is offered) and **IPv6-only docker networks**
  (`docker network create --ipv6 --ipv4=false`) don't work at all. netshim
  fails before the VM boots.
* **Dual-stack** pods and docker networks boot, but only with IPv4. The
  guest silently has no IPv6, even though the pod has an IPv6 address,
  Services have IPv6 ClusterIPs, and peers may resolve the pod by its AAAA
  record. Anything that reaches the pod over IPv6 hits the guest's MAC, and
  the guest drops it because it holds no such address.

### 1.1 Prerequisite: a guest kernel with IPv6 (done)

Granule's bundled guest kernel used to be the `cloud-hypervisor/linux`
release binary for `GRANULE_KERNEL_VERSION`, downloaded as-is. That
binary is `make ch_defconfig` and nothing more, and at
`ch-release-v6.16.9-20260508` the tag's own `arch/x86/configs/ch_defconfig`
and `arch/arm64/configs/ch_defconfig` both say `# CONFIG_IPV6 is not set`.
The binary confirms it: its symbol table has `inet_init` and
`ip_auto_config` but no `inet6_init`, `addrconf_init` or `ip6_input`.
`CONFIG_MODULES` is unset too, so no module could add IPv6 later. A guest
booted on it had no `/proc/net/if_inet6`, and
`test/granulefiles/03-pack-a-rootfs` had to delete nginx's
`listen [::]:80` to get nginx to start.

Of the three ways out (build our own kernel from the same tree, get IPv6
into the upstream config, or make v6 a `GUEST_KERNEL_PACKAGE`-only
feature), granule took the first, because it's the only one granule
controls. The Dockerfile's `fetch-kernel-source` stage now downloads that
tag's source tarball, pinned by `GRANULE_KERNEL_SHA256`. `build-kernel`
then builds it the way the tag's own release workflow does
(`make ch_defconfig`), with a fragment merged over the config:
`CONFIG_IPV6=y`, `CONFIG_IPV6_SIT` off (otherwise every guest gets a
`sit0`), and `CONFIG_IKCONFIG_PROC`, so `/proc/config.gz` answers this
question next time. The stage cross-compiles on the build platform. It
takes about three and a half minutes on 4 vCPUs when cold, and nothing
once the layer is cached. The nginx workaround is gone.

Until steps 2-4 below land, a v6-capable guest on a v4-only splice
behaves like this:

* The guest autoconfigures a link-local address on each NIC and sends
  DAD, RS and MLD. The splice and control guards drop all IPv6 from the
  guest (`spliceGuest`, `guardControlLink`), so none of it leaves the pod.
* ext → tap is still an unconditional wire. On a segment where something
  sends router advertisements, the guest's default `accept_ra=1` and
  `autoconf=1` would give it a SLAAC address and a v6 default route that
  the guard then black-holes. A client that tries AAAA first and doesn't
  fall back quickly would stall. Docker's bridge doesn't send RAs, and as
  far as we know neither do the CNIs section 8 tests against. Step 4's
  `accept_ra=0` (section 3.3) closes this.
* Snapshots: a restore replays the guest's RAM, kernel included, from
  the snapshot and doesn't boot `vmlinux` again (`pkg/hypervisor`'s
  `BuildArgs`). So an image carrying a snapshot keeps the kernel it was
  taken under, with no IPv6, until it's rebuilt. Nothing in the guest
  contract (`pkg/guestimage/contract.go`) moves: the kernel belongs to
  the VMM half (`guestimage.RuntimeKernel`), and the contract covers the
  guest half.

## 2. Goals and non-goals

Goals:

* The guest takes over the pod's IPv6 identity the same way it takes over
  the IPv4 one: the global address and prefix, the link-local address, the
  routes (including the default route), and the MAC. The MAC is already
  shared.
* Single-stack v4, single-stack v6 and dual-stack all come out of the same
  discovery, with no flag needed to choose.
* The splice guard keeps its promise for IPv6: a guest can speak only as
  its own identity, which here means no NDP spoofing and no rogue router
  advertisements.
* A v4-only sandbox's behaviour stays identical: same command line, same
  filters, same tests.

Non-goals:

* SLAAC or DHCPv6 inside the guest. The identity is static and read off
  the namespace, exactly like v4.
* Taking over more than one global address per family. No supported
  runtime assigns several. If one does, we take the first, as v4 does
  today.
* An IPv6 control link (see [section 6](#6-control-link)).

## 3. What the guest needs, and how it gets it

### 3.1 Discovery

`Identity` gains a v6 half, and the v4 half becomes optional:

```go
type Identity struct {
	Iface   string
	MAC     net.HardwareAddr
	MTU, TapMTU int

	// IPv4: nil IP when the interface has no IPv4 address.
	IP      net.IP
	Mask    net.IPMask
	Gateway net.IP
	Routes  []Route

	// IPv6: nil IP6 when the interface has no global IPv6 address.
	IP6        net.IP     // the global (or ULA) address the runtime assigned
	Prefix6    int        // its prefix length
	LinkLocal6 net.IP     // the interface's fe80:: address, taken over verbatim
	Routes6    []Route    // the interface's IPv6 main-table routes, ordered as for v4
}
```

`DiscoverIdentity` reads both families.

* **Global address.** The first `FAMILY_V6` address with universe scope
  that is neither `IFA_F_TEMPORARY` (privacy), `IFA_F_DEPRECATED` nor
  `IFA_F_DADFAILED`. ULAs (`fd00::/8`, which is what kind uses) count as
  global here. They are routed, not link-scoped. An address that is still
  `IFA_F_TENTATIVE` is taken anyway: the runtime assigned it, and its own
  DAD is about to become irrelevant (see 3.4).
* **Link-local address.** The first scope-link `fe80::/10` address. The
  guest is given this exact address rather than left to derive one. A
  point-to-point CNI's host side may already have the pod's link-local
  address in its neighbour cache, and the guard (section 4) needs to know
  in advance which link-local source to allow. Generating it in the guest
  depends on `addr_gen_mode` (EUI-64 versus stable-privacy) and could
  disagree with both.
* **Routes.** `RouteList(link, FAMILY_V6)` goes through the same
  `carriedRoutes` treatment, made family-aware. `normalizeDst` takes a
  family, and the default destination becomes `::/0`. On top of the
  v4 rules, it drops:
  * `fe80::/64`. The guest's kernel installs that route itself when the
    link-local address is added.
  * `ff00::/8` and other multicast or `RTN_LOCAL` entries. The existing
    "plain unicast only" rule already covers these.

  Gateways are commonly link-local: Calico uses `fe80::ecee:eeff:feee:eeee`,
  and Cilium and EKS use the host veth's link-local address. A link-local
  gateway is fine, because every carried route is installed with
  `dev <iface>`.
* **Errors.** It fails only when the interface has **neither** an IPv4
  address nor a global IPv6 one: "no IPv4 or IPv6 address found on
  <iface>".

`pickExternalIface` considers default routes of both families, so a
v6-only pod resolves to the interface carrying `::/0`. If the v4 and v6
default routes resolve to *different* interfaces (a Multus pod, say),
that's ambiguous in the same way two v4 defaults are. It's reported as an
error that names both and asks for `NETSHIM_EXTERNAL_IFACE`. `HasIPv4`
becomes "has a global address of either family". Link-local addresses never
count, because every interface has one, `granule0` included.

**Opt-out.** A new `NETSHIM_IPV6` setting (`auto`, the default, or `off`;
also `granule vm create -ipv6`) discards the v6 half after discovery. That
gives an operator today's exact behaviour back on a dual-stack pod. It's
the obvious lever for a guest kernel without IPv6, and for any regression
found after rollout. With `off`, the guard stays v6-closed.

### 3.2 Carrying it: new command-line parameters

The kernel has no `ip6=` autoconfiguration (`CONFIG_IP_PNP` is IPv4 only),
so the guest's own userspace has to do it, like it already does for routes
and DNS. Three new parameters, all in granule's namespace:

```
granule.ip6=2001:db8:1::5/64,fe80::42:acff:fe11:5
granule.routes6=2001:db8:1::/64,::/0@fe80::1
granule.dns=fd00:10:96::a,10.96.0.10
```

* `granule.ip6=<global>/<plen>[,<link-local>]`: the global address, then
  the link-local address. When the namespace has a global v6 address, the
  link-local one is always present, but it's optional in the syntax for a
  hand-written command line. An empty global field (`granule.ip6=,fe80::…`)
  isn't produced. A link-local-only identity carries nothing worth taking
  over.
* `granule.routes6=`: the same `<dst>[@<gw>]` grammar as
  `granule.routes=`. The `@` separator was chosen so it would survive the
  colons in IPv6. This is a **separate parameter, not more of
  `granule.routes=`**, because of compatibility. An existing guest image
  (contract 2) handed a mixed list would try `ip route replace ::/0 …`,
  which fails on a kernel without IPv6. Its route script then sets
  `failed=1` and skips its prune pass, so the v4 table that works today
  would degrade. An old image ignores a parameter it has never heard of,
  and that's the property we want.
* `granule.dns=`: see section 7.

The device is still read from `ip=` field 6, and defaults to `eth0`. A
v6-only guest has no `ip=` at all and gets `eth0`, which is what
`GuestConfig` names anyway. The scripts already read the device this way.

`WithGuestParams` treats `granule.ip6=` as an identity parameter alongside
`ip=`. An explicit `ip=` **or** `granule.ip6=` in `CHV_CMDLINE` means an
operator is addressing the guest by hand, so none of the derived identity
parameters of either family are appended. The reasoning is the one already
documented there: half of a derived identity on top of a hand-written one
is the part most likely to be wrong.

**Budget.** The x86 kernel command line is capped at 2048 bytes. A
dual-stack kind pod adds about 150 bytes:
`granule.ip6=` with two addresses is about 60, and `granule.routes6=` with
three routes is about 90. That fits comfortably, but `GuestConfig` should
assert the final length and log a warning when it passes the cap, rather
than letting the kernel truncate it silently.

### 3.3 Applying it in the guest

A new script, `/usr/local/libexec/granule-configure-ip6`, goes in
**overlay-common**, run by a systemd unit on Debian and an OpenRC service
on Alpine. It is not part of `granule-configure-net`, which exists only on
Debian, and only for distro kernels that lack `CONFIG_IP_PNP`. The bundled
kernel's IPv4 path never runs a script at all, but IPv6 always needs one.
It runs before `granule-net-routes` and after the device exists, waiting
for it the same way the route script does. In order:

1. If there's no `granule.ip6=`, exit 0. If there's no
   `/proc/net/if_inet6` (a kernel without IPv6), print one line to the
   console saying which address the pod has that this guest can't take,
   then exit 0. The v4 half must still come up, so this is not a failure.
2. `sysctl net.ipv6.conf.<dev>.accept_ra=0`, `autoconf=0` and
   `use_tempaddr=0`. The guest's v6 table is exactly the carried one,
   for the same reason the v4 one is. An RA from anyone else on a shared
   segment (another, non-granule container on docker's bridge) must not
   install a default route or a SLAAC address. A SLAAC address would be
   an address the guard drops anyway (section 4).
3. `ip link set dev <dev> addrgenmode none`, so the kernel doesn't invent
   a link-local address of its own. This has to happen before the link is
   up, or be followed by flushing the generated address. The script handles
   both cases, because on the bundled kernel `ip=` has already brought
   `eth0` up by then.
4. `ip -6 addr replace <link-local>/64 dev <dev> nodad` and
   `ip -6 addr replace <global>/<plen> dev <dev> nodad`. See 3.4 for why
   `nodad`. When `granule.routes6=` is present the global address also
   gets `noprefixroute`. Otherwise the kernel installs an on-link route
   for the whole prefix at metric 256. The carried route for that same
   prefix goes in at metric 1024, so it can neither replace nor outrank
   the kernel's route, and the prune keeps both because the prefix
   matches. On a point-to-point CNI (kindnet, Cilium), where the pod
   reaches its own /64 through a gateway, the guest would then
   neighbour-solicit for every other pod and reach none of them over v6.
   The end-to-end kind run (8.3) found this. IPv4 doesn't need it,
   because `route replace` takes over the kernel's metric-0 route.
5. Bring the link up, if it wasn't already.

`granule-configure-routes` then learns `granule.routes6=`. It's the same
two-pass install with `ip -6 route replace`, and the same prune with
`ip -6 route show dev <dev>`, where `::/0` maps to `default`, `/128` is
stripped, and `fe80::/64` is always kept. So is every route printed with
a type before it, such as the kernel's `multicast ff00::/8`, which
neighbour discovery needs. Doing it in the existing script
keeps the v4 and v6 routing logic side by side. The v6 pass's failures
mustn't stop the v4 prune, and the reverse is also true: each family keeps
its own `failed`.

A guest-contract bump (2 → 3) records that the image understands these
parameters. `granule` warns when it boots a contract-2 image into a
namespace whose identity has IPv6: "this guest image does not configure
IPv6; it will have none". For a v6-only identity, which would be a guest
with no network at all, the warning says that instead.

### 3.4 DAD and NDP when the guest reuses the pod's MAC and addresses

The namespace and the guest end up holding the same MAC, the same
link-local address and the same global address. This is the v4 situation
again, and it works for the same reason: the splice steals all ingress on
the external interface, so the namespace's copy never receives anything.
Nothing on the segment can ever see two owners. IPv6 adds a few behaviours
that need handling:

* **Duplicate address detection would only ever hurt.** The runtime's IPAM
  guarantees uniqueness, not the segment, and the namespace's own copy of
  the address is invisible behind the splice. So DAD can't find a real
  conflict. What it can do is:
  * Delay the guest by `DupAddrDetectTransmits × RetransTimer`, about 1s.
    During that time the address is tentative and unusable, which makes
    boot and readiness slower.
  * **Fail falsely and permanently.** On docker with
    `--userland-proxy=false`, bridge ports are in hairpin mode, so the
    guest's own multicast NS comes back to it. Kernels with RFC 7527
    enhanced DAD (`enhanced_dad=1`, the default since 4.x) recognise their
    own nonce and ignore it. A guest kernel or configuration without that
    marks the address `dadfailed` and never retries, which leaves a guest
    permanently without its v6 address.

  So both addresses are installed with `nodad`. The guard still passes the
  RFC 4862 DAD probe shape (NS from `::`) and MLD from `::`, so a guest
  image that does its own DAD still works. It's slower, but it works.
* **The namespace's stack keeps its addresses**, as it does for v4. It
  keeps the address so the VM container can read the identity back off the
  interface. Its own DAD finished before netshim ran, or finishes afterward
  with nobody answering, so its address never fails. It will still send NS
  for any v6 traffic that something inside the pod namespace originates. The
  replies go to the guest, which ignores a solicited NA it didn't ask for.
  This is exactly how v4 ARP behaves today.
* **Neighbour discovery from outside** works with no help. The gateway (or
  the docker bridge) sends an NS for the pod's address to its
  solicited-node multicast group, with a destination MAC of `33:33:ff:xx:xx:xx`.
  ext → tap is unconditional, so the NS reaches the guest, and the guest
  answers with an NA from its own MAC. The guard passes that NA.
* **MLD.** The guest joins its solicited-node groups and sends MLDv2
  reports. On a segment with MLD snooping and a querier, the guest's own
  joins must get out, or multicast NS stops reaching it once the
  namespace's original membership times out. Docker's bridge has
  `multicast_snooping=1` but no querier, so it floods, and cloud VPCs don't
  snoop. The guard still passes MLD reports, so snooping environments keep
  working.
* **The NA "override" flag.** The guest's unsolicited NAs carry it. That's
  safe because the guard holds the target to the guest's own addresses.
* **MTU.** The splice MTU is the interface's MTU, as today. IPv6 needs at
  least 1280, and the guest gets whatever the pod had.

## 4. The splice guard for IPv6

### 4.1 What the original drop protects against

The comment above `spliceGuest` names the threat, and it's the same as
ARP's. The guest is untrusted, and under the docker backend it shares a L2
segment (docker's `bridge`) with every other VM container on the host and
with the gateway that the git and model proxies answer on. If IPv6 were
passed **unfiltered**, a guest could:

1. **Spoof neighbour advertisements.** It could send an NA with the override
   flag for the gateway's address, or a neighbour's, and redirect their
   traffic to itself. This is ARP poisoning in IPv6 clothes.
2. **Send rogue router advertisements** (RFC 6104). Every host on the
   segment that accepts RAs would get a new default router (the guest) or a
   new prefix. This is **worse than anything ARP can do**: one frame
   reconfigures every IPv6-enabled neighbour, including ones that never
   talk to the guest. Most container namespaces have `accept_ra=1`
   whenever forwarding is off.
3. **Send ICMPv6 redirects** to a neighbour, re-pointing that neighbour's
   route for some destination at the guest.
4. **Spoof its source address.** It could send from another sandbox's v6
   address, or from any address at all.
5. **Hide any of the above behind extension headers.** Hop-by-hop,
   destination-options or routing headers move the ICMPv6 header off the
   fixed offset that a u32 match would look at. RFC 6980 makes receivers
   drop *fragmented* NDP, and Linux does. Nothing makes them drop NDP that
   follows an unfragmentable extension header.

The drop closes all five of these today, on any segment that carries IPv6,
including a v4-only identity on a dual-stack network. **When the identity
has no IPv6, the drop stays exactly as it is.** That's the rule that keeps
this from being reopened carelessly. The v6 filters below are installed
only when `Identity.IP6` is set.

### 4.2 What passes when the guest has an IPv6 identity

These are all `protocol 0x86DD` u32 filters on the tap's ingress, ahead of
the existing final drop, and every one of them also matches the source MAC
(the existing `srcMAC` keys). Offsets are from the IPv6 header: next header
is at 6 (in the word at 4, mask `0x0000ff00`), source at 8–23, ICMPv6 type
at 40 when there are no extension headers, the NS/NA target at 48–63, and
the first ND option at 64.

The allow rules (redirect to ext):

| # | What | Match |
|---|---|---|
| a | NA for our own address | nh=58, type 136, src ∈ {global, link-local}, target = that same address, first option = TLLA (type 2, len 1) carrying our MAC. One filter per address. |
| a′ | Option-less NA for our own address | As a, but with payload length 24 (no options at all) instead of the TLLA check. Linux answers a *unicast* NS (a reachability probe) with an NA carrying no options, and without this rule those probes go unanswered. An NA without a TLLA can't change anyone's link-layer address, and its target is still held to ours. One filter per address. |
| b | NS from our own address | nh=58, type 135, src ∈ {global, link-local}, first option = SLLA (type 1, len 1) carrying our MAC. One filter per address. |
| c | DAD probe | nh=58, type 135, src = `::`. It claims nothing, like the ARP probe from 0.0.0.0. |
| d | MLD report | nh=0 (hop-by-hop), HBH next header 58 and length 0 at 40, ICMPv6 type 143 (MLDv2) or 131 (MLDv1) at 48, src ∈ {link-local, `::`}. |

The drop rules (`TC_ACT_SHOT`), placed after a–d:

| # | What | Match |
|---|---|---|
| e | Every other NDP or router message | nh=58, type ∈ {133 RS, 134 RA, 135 NS, 136 NA, 137 redirect, 138 router renumbering, 148/149 SEND, 151–153 multicast router discovery}, one filter per type. RS is dropped because the guest has `accept_ra=0` and has nothing to solicit. |
| f | Extension headers that can hide NDP | nh ∈ {0 hop-by-hop, 43 routing, 60 destination options}. Fragment (44) stays allowed: large UDP over IPv6 needs it, and fragmented NDP is dropped by receivers (RFC 6980). |

Then one more allow rule, and the existing final drop:

| # | What | Match |
|---|---|---|
| g | Anything else from our addresses | src ∈ {global, link-local} |
| — | existing final drop | `ETH_P_ALL` |

**Limit on the TLLA/SLLA checks.** The option checks are deliberately
"first option only". Linux always puts the link-layer option first and
alone, and the Linux receiver uses the first instance of a duplicated
option. The source-MAC match already makes a mismatched option useless for
redirecting traffic to anyone but the guest itself. The option check only
stops the guest from pointing a neighbour's cache entry for *its own*
address at another sandbox's MAC.

Rule g is wider than v4's per-protocol shape, and that's deliberate. Once
NDP and the header-hiding cases are gone, a source-address match is what
the v4 rule does too: any IP protocol from the guest's address.

All of this stays within u32. That matters for the same reason it does
now: `cls_flower` and `cls_matchall` are often missing from minimal
kernels, and `cls_bpf` needs more than `CAP_NET_ADMIN`. The rule count is
roughly 20 filters. The u32 hash table isn't needed at that size.

ext → tap stays unconditional. What the segment sends the guest isn't the
guard's business, and the guest's `accept_ra=0` (section 3.3) handles
rogue RAs arriving from the segment.

`spliceGuest`'s precondition changes from "a 6-byte MAC and an IPv4
address" to "a 6-byte MAC and at least one of IPv4 and IPv6". For a
v6-only identity, the IPv4 and ARP allow rules aren't installed, so all
IPv4 and ARP from the guest is dropped.

## 5. The external interface's own IPv6

Nothing changes here. The splice leaves the external interface addressed,
in both families, so `Discover` in the VM container can read the identity
back off it. Its v6 addresses are as inert as its v4 one: it never receives
a frame.

## 6. Control link

The control link stays **IPv4, link-local, `169.254.100.0/24` on
`granule0`**, whatever the pod's family. In detail:

* It's a private L2 segment between two kernels that both always have
  IPv4: the namespace's, and the guest's (the bundled kernel is if
  anything *more* certain to have v4 than v6). It's never routed and never
  leaves the pod, so the cluster's address family is irrelevant to it. A
  v6-only cluster has no IPv4 for it to collide with.
* `NETSHIM_CONTROL_CIDR` keeps rejecting IPv6, and `guardControlLink` keeps
  dropping it. The comment there already says why: the bridge's link-local
  address would otherwise be the same route-out that the v4 guard closes.
* **Noise.** With a v6-capable guest kernel, `eth1` and `granule0` each
  autoconfigure a link-local address and send DAD, RS and MLD. The control
  guard drops what the guest sends, so it's harmless, but it's pointless
  and it shows up in captures. Two things fix it:
  * netshim sets `addrgenmode none` on `granule0` and the control tap with
    `netlink.LinkSetIP6AddrGenMode`. That's netlink, so it needs only
    `CAP_NET_ADMIN`, unlike `disable_ipv6`, whose `/proc/sys` is read-only
    in an unprivileged container. It has to be done before the link is
    first brought up.
  * `granule-control-net` sets `addrgenmode none` on `eth1`.
* `pickExternalIface` already excludes netshim's own links by name, and
  under the rule above link-local addresses don't count as "addressed". So
  a restarted VM container never mistakes `granule0` for the external
  interface on a v6-only pod.
* The memory agent and anything else on the control link are unaffected.
  `granule exec` uses vsock.

## 7. DNS

DNS is where a v6-only guest breaks even after addressing works:

* `GuestResolvers` keeps IPv4 nameservers only, and `ParseDNS` rejects
  IPv6. On a v6-only cluster the pod's resolv.conf names the kube-dns
  ClusterIP, which is IPv6 (kind uses `fd00:10:96::a`). Nothing passes, and
  the guest gets `FallbackDNS`, `8.8.8.8`, which it can't route to because
  it has no IPv4. The result is the "open egress, every lookup hangs"
  failure that `FallbackDNS`'s comment describes.
* `ip=`'s dns0/dns1 fields can't carry IPv6, because they are separated by
  colons.

The proposal:

* **A new `granule.dns=` parameter** with the ordered nameserver list of
  either family: up to 3, which is glibc's and musl's `MAXNS`. resolv.conf
  order matters, and splitting the list by family would lose it.
  `granule-configure-dns` prefers `granule.dns=` when it's present, and
  stops exiting early when there's no `ip=`, since a v6-only guest has
  none. `ip=`'s dns0/dns1 keep carrying the IPv4 subset, so a contract-2
  image keeps the resolver it gets today. The search list and options are
  written beside nameservers from either source.
* `ParseDNS` (`NETSHIM_DNS`, `-dns`) accepts IPv6, with up to 3
  nameservers in total and at most 2 IPv4 ones (for `ip=`).
* `GuestResolvers` becomes family-aware. It keeps nameservers of the
  families the guest actually has. For IPv6 it drops `::1`, `::` and
  `fe80::/10`, which would need a zone index that resolv.conf can't carry
  portably, and which isn't on any segment the guest is on anyway. It keeps
  global and ULA addresses. On a dual-stack pod, a v4 and a v6 cluster DNS
  address both pass, in the pod's order.
* **`FallbackDNS` becomes per-family.** It's `8.8.8.8` when the guest has
  IPv4 and `2001:4860:4860::8888` when it's v6-only. On a dual-stack pod it
  stays `8.8.8.8`, which is unchanged behaviour. The docker case (embedded
  `127.0.0.11`) on a v6-only network lands on the v6 fallback. That only
  works if the host has v6 egress, which a v6-only docker network implies
  anyway. The log line names which fallback was used.

## 8. Test plan

### 8.1 Unit tests (no root)

* `normalizeDst`, `carriedRoutes`, `defaultGateway` with v6 dumps. These
  check that `::/0` is synthesized, that `fe80::/64` and multicast are
  dropped, that the ordering is on-link first and then most specific, and
  that link-local gateways are kept.
* `pickExternalIface`: a v6-only namespace (`::/0` only); dual-stack with
  both defaults on one interface; v4 and v6 defaults on different
  interfaces, which is an error naming both; and `granule0` with only a
  link-local v6 address, which is never picked.
* `GuestConfig` / `WithGuestParams`: the v4-only output is **byte-identical
  to today**, and that's a golden test. Also the v6-only output (no `ip=`,
  `granule.ip6=` + `granule.routes6=`), the dual-stack output, an explicit
  `ip=` or `granule.ip6=` suppressing everything, `NETSHIM_IPV6=off`, and
  the 2048-byte warning.
* `ParseDNS`, `GuestResolvers`, `ResolveDNS` across v4-only, v6-only and
  dual-stack resolv.conf files, plus the per-family fallback.
* The guest scripts, in the style of `guestnetscript_test.go` (fake
  `/proc/cmdline`, stubbed `ip`/`sysctl` recording calls):
  * `granule-configure-ip6`: `nodad`, `addrgenmode none`, `accept_ra=0`;
    no-op without the parameter; one console line and exit 0 without
    `/proc/net/if_inet6`.
  * `granule-configure-routes` with `granule.routes6=`: two passes, the
    prune maps `::/0` to `default` and keeps `fe80::/64`, and a v6 failure
    doesn't block the v4 prune.
  * `granule-configure-dns` with `granule.dns=` and no `ip=`.
* The shellcheck and hardening tests that already cover the overlay
  scripts pick up the new script.

### 8.2 netns tests (`GRANULE_NETNS_TESTS=required`, root)

These go next to `guard_test.go`, `splice_test.go` and
`control_guard_test.go`, built in the private namespace that `TestMain`
already provides. Each test builds a veth pair to stand in for the CNI, and
addresses it with `nodad` so it doesn't wait.

* **v6-only namespace.** ext has `2001:db8:1::5/64` and its link-local
  address, plus `default via fe80::1`. `DiscoverIdentity` returns no v4,
  the right `IP6`, `LinkLocal6` and `Routes6`, and `Setup` succeeds.
* **Dual-stack namespace.** The above plus `172.17.0.5/16`. Both halves
  are discovered, and the v4 guard rules are unchanged (the existing guard
  test runs as-is against it).
* **v6 guard test** (`TestSpliceGuest_HoldsGuestToItsIdentityV6`), using
  the same forged-first, legitimate-last method as the v4 test.
  * Must not arrive on the far end:
    * an NA for the gateway's address;
    * an NA for our address with a foreign TLLA;
    * an NA from a forged source MAC;
    * an NS from a foreign source;
    * an RA, a redirect, and an RS;
    * an RA behind a hop-by-hop header, and one behind a
      destination-options header;
    * a UDP packet from another sandbox's v6 address;
    * a packet from a link-local address that isn't ours.
  * Must arrive:
    * our NA and NS;
    * a DAD NS from `::`;
    * an MLDv2 report;
    * UDP from the global address and from the link-local address;
    * a fragmented UDP datagram.
* **v4-only identity on a v6-carrying segment.** Every 0x86DD frame is
  still dropped. This is the regression test for section 4.1.
* **Control link.** `granule0` and the control tap have no link-local
  address after `Setup` (`addrgenmode none`).

### 8.3 End to end

* **docker.** Two networks:
  * a dual-stack one (`docker network create --ipv6 --subnet
    fd00:dead:beef::/64 …`);
  * a v6-only one (`--ipv4=false`, which needs docker 27 or later).

  On each, `granule vm run` checks:
  * the guest holds the container's v6 address (`ip -6 addr` over `granule
    exec`);
  * it can `ping -6` the gateway;
  * a peer container reaches a guest port over v6;
  * a `-p` binding published on `[::]` reaches the guest;
  * name resolution works, with the v6 fallback on the v6-only network.

  This fits the nightly suite (`docs/nightly-test-suite.md`) if the CI
  host's docker daemon has IPv6 enabled (`"ipv6": true`, plus
  `ip6tables`). That needs checking on the runner image.
* **kind.** A dual-stack cluster (`networking.ipFamily: dual`) and a
  v6-only one (`ipFamily: ipv6`), each running a granule pod.
  * The pod's v6 address answers from the guest.
  * A v6 ClusterIP Service in front of it works.
  * The guest resolves `kubernetes.default` through the v6 kube-dns.
  * Egress to another pod works over v6, which is kindnet's
    point-to-point routes with a link-local gateway.
* **GKE.** A dual-stack cluster (`--stack-type=ipv4-ipv6` on a dual-stack
  subnet) in `grain-sandbox-dev`, with the same checks as kind, plus
  Dataplane V2 (Cilium) routes. IPv6-only GKE only if the project and
  region offer it.

Done: `test/e2e-ipv6.sh` (targets `docker`, `kind`, `kind-dual`,
`kind-ipv6` and `gke`). It runs nightly for docker and kind
(`.grain/workflows/e2e-ipv6.yml` and stage 5 of
`test/run-nightly-suite.sh`) and by hand for GKE
(`.grain/workflows/e2e-ipv6-gke.yml`); `docs/nightly-test-suite.md`
section 5 has the details. Where it falls short of the plan above:

* On docker the guest can't use docker's resolver, 127.0.0.11, which is
  the container's own loopback. So "name resolution works" is checked in
  three parts. First, the guest is given only nameservers of a family it
  has, which on the v6-only network means the v6 fallback. Second, the
  guest resolves a name over v6 from a CoreDNS peer on the same network.
  Third, it resolves a public name through its own resolver, but only
  when the host has a route off the box for that family; otherwise that
  check is a SKIP.
* On a dual-stack cluster kube-dns is IPv4, so "through the v6 kube-dns"
  goes through a SingleStack IPv6 Service in front of the kube-dns pods.
  On a v6-only cluster it goes through the guest's own resolv.conf.
* GKE has no IPv6-only stack type (`--stack-type` takes `ipv4` or
  `ipv4-ipv6`), so there's no v6-only GKE run.

First run, 4 October 2026, on the alpine guest:

* docker 29.8: 14 pass. One SKIP: the sandbox host has no v6 default
  route, so the guest's v6 fallback resolver can't be reached.
* kind v0.32 (Kubernetes 1.36, kindnet): dual 5/5 and ipv6 5/5, with the
  peer pod on the other node.
* GKE 1.35.8 dual-stack, Dataplane V2 (`us-central1-a`, private nodes,
  ULA internal IPv6): 5/5 across two nodes. There the guest's v6 routes
  are a /128 for its own address and a link-local default gateway.

The run found two bugs in the guest scripts, both fixed in the same
change (see 3.3). First, the guest's global address kept the kernel's
own on-link /64, which outranked the carried route through the gateway,
so on kindnet no pod could reach another over v6. Second, the prune read
the kernel's `multicast ff00::/8` route's type as a prefix.

## 9. Implementation plan

The follow-up tasks are listed below. Each lists what it waits for.

1. **Guest kernel with IPv6.** Done: the bundled kernel is now built
   from the `GRANULE_KERNEL_VERSION` tree with `CONFIG_IPV6=y`, and the
   nginx `[::]` workaround in `test/granulefiles/03-pack-a-rootfs` is
   gone. See section 1.1.
2. **v6 discovery.** The `Identity` v6 fields, family-aware
   `carriedRoutes`/`normalizeDst`, `DiscoverIdentity` reading both
   families, `pickExternalIface` on both families' default routes, and
   `NETSHIM_IPV6=off`. It doesn't yet carry the identity to the guest or
   open the guard. Its only visible effect is that v6-only pods get a clear
   "IPv6-only identity; the guest cannot take it yet" error instead of
   today's "no IPv4 address". Includes the unit tests and the
   v6-only/dual-stack netns discovery tests. *Waits for: this design.*
3. **v6 splice guard.** Done: `ipv6GuardRules` in pkg/netshim/splice.go,
   tested by `TestSpliceGuest_HoldsGuestToItsIdentityV6` and
   `TestSpliceGuest_IPv4IdentityDropsAllIPv6`. The section 4 filters, installed only when
   `Identity.IP6` is set, plus the 8.2 guard tests, including the
   v4-identity-on-v6-segment regression test. *Waits for: 2.*
4. **Guest-side configuration.** Done: `ip6Param` and
   `WithGuestParams` in pkg/netshim/guest.go, `granule-configure-ip6`
   (`granule-net-ip6.service` and `/etc/init.d/granule-net-ip6`), the
   `granule.routes6=` pass in `granule-configure-routes`, guest contract
   3 (`guestimage.ContractIPv6`, with `ipv6ContractWarning` in
   cmd/granule-shim), and `noLinkLocal6` on the control link. The
   "IPv6-only identity" refusal step 2 added is gone. `granule.ip6=` and `granule.routes6=` in
   `GuestConfig`/`WithGuestParams`, the new `granule-configure-ip6` script
   and its systemd and OpenRC units, `granule.routes6=` in
   `granule-configure-routes`, the guest contract bump to 3 with a
   boot-time warning for older images, and `addrgenmode none` on the
   control link. *Waits for: 2 and 3.* Together with 3, this is what
   actually turns IPv6 on, so it must not land before the guard.
5. **v6 DNS.** Done: `dnsParam` (`granule.dns=`, emitted only when
   `ip=` can't carry the whole list, so the v4-only command line is
   unchanged), family-aware `ParseDNS`, `GuestResolvers` and
   `FallbackDNS` in pkg/netshim, `ResolveDNS` now run after discovery so
   it knows the guest's families, and `granule-configure-dns` reading
   `granule.dns=` with or without `ip=`. *Waits for: 4.*
6. **End-to-end coverage.** Done: see 8.3. The docker dual-stack and v6-only tests, kind
   dual-stack and v6-only, and GKE dual-stack if available, plus wiring
   whatever can run unattended into the nightly suite. *Waits for: 1, 4
   and 5.*
