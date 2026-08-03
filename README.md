# ba6

`ba6` is a dependency-free Linux/amd64 multicall binary containing a focused
set of Unix utilities. Invoke an applet as `ba6 cat file`, or create a symlink
whose basename is an applet name.

Run `ba6 --list` to list applets, `ba6 help COMMAND` for command-specific help,
or `ba6 COMMAND --help` for the same documentation.

## Included applets

The binary currently includes:

```text
[ base64 basename blkid cat chgrp chmod chown cmp cp curl cut date df diff dirname
dmesg du echo env expr false find free grep gunzip gzip halt head help hexdump hostname id
ip iptables kill ln ls mkdir mknod mount mv nano nc nslookup od pgrep pidof ping pkill
poweroff printenv printf ps pwd readlink realpath reboot rm rmdir sed seq sh sha256sum sleep sort ss
stat strings sync tail tar tee test touch tr true umount uname uniq uptime wc wget
which whoami xargs
```

The filesystem and scripting set includes hard and symbolic links, canonical
path resolution, recursive octal permission and ownership changes, file
metadata formatting, pipeline fan-out, path component extraction, conditional
expressions, time display, and duration-based sleeping. `date` intentionally
does not change the system clock.

The extended recovery set also includes SHA-256 verification, filesystem and
directory usage, recursive search, stream editing, protected tar extraction,
gzip compression, system identity, `/proc` process and memory reporting, and
signal delivery. `find` deliberately has no `-exec`, and `sed` has no in-place
mode, preserving an explicit-write model for those individual applets.

The scripting and diagnostic set adds a small execution-capable shell, `xargs`,
environment and expression tools, byte and text inspection, process matching,
kernel logs, uptime, socket inspection, DNS, ICMP, HTTP(S), and TCP/UDP copying.
Storage recovery includes filesystem signature probing, node creation, mounting,
unmounting, buffer flushing, and privileged halt/reboot/poweroff controls.
`nano` is a compact full-screen editor with
navigation, insertion/deletion, line cutting, saving (`Ctrl-S`), and guarded
exit (`Ctrl-X`).

## Build and verify

The supported release target is Linux/amd64. The canonical build is static and
installs a seccomp filter at process startup. For environments that cannot use
the filter, invoke the binary as `ba6 --no-seccomp COMMAND ...` or
`ba6 --seccomp=off COMMAND ...`. Other startup protections remain enabled.

Applets that genuinely require a syscall denied by the normal filter
automatically skip seccomp while retaining `no_new_privs` and core-dump
protection. This applies to process-executing (`sh`, `xargs`, and `env`), mount,
and ordinary network client/server applets. Other applets, including `nano`,
continue to run with the filter enabled.

```sh
make build
make verify
```

`make verify` checks formatting, runs unit/regression tests, vet, the configured
linters, and the static-build check.

## Network configuration

The built-in `ip` applet uses Linux rtnetlink directly; it does not execute the
host's `ip` program.

```sh
ba6 ip addr show
ba6 ip addr add 192.0.2.10/24 dev eth0
ba6 ip addr del 192.0.2.10/24 dev eth0

ba6 ip route show
ba6 ip route get 192.0.2.25
ba6 ip route add default via 192.0.2.1 dev eth0 metric 100
ba6 ip route del default via 192.0.2.1 dev eth0 metric 100

ba6 ip neigh show dev eth0
ba6 ip rule show
```

Basic bond and VLAN lifecycle operations are also supported:

```sh
ba6 ip link add bond0 type bond mode active-backup miimon 100
ba6 ip link set dev eth0 master bond0
ba6 ip link set dev eth1 master bond0
ba6 ip link set dev bond0 up

ba6 ip link add link bond0 name bond0.100 type vlan id 100
ba6 ip link set dev bond0.100 up
ba6 ip link set dev bond0.100 mtu 1400 alias tenant-vlan
ba6 ip link show

ba6 ip link set dev eth0 nomaster
ba6 ip link delete bond0.100
ba6 ip link delete bond0
```

Showing addresses and routes normally needs no special capability. Changing
them requires the same `CAP_NET_ADMIN` privilege as iproute2.

The `iptables` applet manages an isolated IPv4 filter table through nftables
netlink without executing a host utility. Listing and changing firewall rules
requires `CAP_NET_ADMIN`:

```sh
ba6 iptables -A INPUT -p tcp --dport 22 -j ACCEPT
ba6 iptables -A INPUT -s 198.51.100.0/24 -j DROP
ba6 iptables -L --line-numbers
ba6 iptables -D INPUT 2
```

## Scope

The applets intentionally implement the options shown by each command's
`--help`; unsupported options are rejected. They are not complete GNU
coreutils, util-linux, procps, iproute2, or full POSIX-shell replacements. The
shell supports quoting, variable expansion, command lists, `&&`/`||`, pipelines,
redirections, and the `cd`, `pwd`, `export`, `unset`, `exit`, and `:` builtins;
it intentionally omits a programming language (`if`, loops, functions, and
command substitution). Destructive commands include same-file, self-copy, and
filesystem-root safeguards.
