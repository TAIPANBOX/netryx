# python3 -I fixdns.py <pid> <name>: in the mount namespace of pid (the
# tapns.py of device <name>), keep /etc/resolv.conf bound to
# /tmp/nxload/resolv.<name> (dnsmasq replaces the file by rename on every
# reload, which drops the bind); one line per rebind in
# /tmp/nxload/fixdns.log; ends when /tmp/nxload/stop exists
import ctypes, os, sys, time
p, name = sys.argv[1], sys.argv[2]
fd = os.open("/proc/%s/ns/mnt" % p, os.O_RDONLY); os.setns(fd, os.CLONE_NEWNS); os.close(fd)
libc = ctypes.CDLL(None, use_errno=True)
src = ("/tmp/nxload/resolv.%s" % name).encode()
while not os.path.exists("/tmp/nxload/stop"):
    try:
        ok = "192.168.124.1" in open("/etc/resolv.conf").read()
    except OSError:
        ok = False
    if not ok:
        r = libc.mount(src, b"/etc/resolv.conf", None, 4096, None)
        with open("/tmp/nxload/fixdns.log", "a") as f:
            f.write("%s %s rebind %d\n" % (time.strftime("%T"), name, r))
    time.sleep(1)
