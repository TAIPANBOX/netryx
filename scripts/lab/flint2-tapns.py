# Runs ON flint2 with python3 -I. A house device made of a network namespace:
# the router's busybox ip has no netns and the kernel no veth, so two TAP
# devices and this process stand in for a veth pair. nxl<name> is a port of
# br-lan in the router's namespace; nxh<name> lives in a namespace of its own
# (network and mount) with <mac> and <ip>/24, default route and resolver
# 192.168.124.1. This process copies frames between the two and starts
# <workers> copies of worker.sh inside the namespace. It ends, and both TAPs
# with it, when /tmp/nxload/stop exists or on SIGTERM.
#   python3 -I tapns.py <name> <mac> <ip> <workers>
import ctypes, fcntl, os, select, signal, struct, subprocess, sys, time

D = "/tmp/nxload"
name, mac, ip, workers = sys.argv[1], sys.argv[2], sys.argv[3], int(sys.argv[4])
TUNSETIFF, IFF_TAP, IFF_NO_PI = 0x400454CA, 0x0002, 0x1000
MS_BIND, MS_REC, MS_PRIVATE = 4096, 16384, 1 << 18
libc = ctypes.CDLL(None, use_errno=True)


def tap(ifname):
    fd = os.open("/dev/net/tun", os.O_RDWR)
    fcntl.ioctl(fd, TUNSETIFF, struct.pack("16sH", ifname.encode(), IFF_TAP | IFF_NO_PI))
    return fd


def run(*a):
    subprocess.run(a, check=True)


def mount(src, dst, flags):
    if libc.mount(src, dst, None, flags, None) != 0:
        raise OSError(ctypes.get_errno(), "mount %s" % dst)


outer = tap("nxl" + name)
run("ip", "link", "set", "nxl" + name, "master", "br-lan")
run("ip", "link", "set", "nxl" + name, "up")

os.unshare(os.CLONE_NEWNET | os.CLONE_NEWNS)
# private first, so nothing below reaches the router's own mounts
mount(None, b"/", MS_REC | MS_PRIVATE)
with open("%s/resolv.%s" % (D, name), "w") as f:
    f.write("nameserver 192.168.124.1\n")
mount(("%s/resolv.%s" % (D, name)).encode(), b"/etc/resolv.conf", MS_BIND)
for k in ("all", "default"):
    with open("/proc/sys/net/ipv6/conf/%s/disable_ipv6" % k, "w") as f:
        f.write("1")
inner = tap("nxh" + name)
run("ip", "link", "set", "lo", "up")
run("ip", "link", "set", "nxh" + name, "address", mac)
run("ip", "addr", "add", ip + "/24", "dev", "nxh" + name)
run("ip", "link", "set", "nxh" + name, "up")
run("ip", "route", "add", "default", "via", "192.168.124.1")

kids = [subprocess.Popen(["/bin/sh", D + "/worker.sh", "%s%d" % (name, i)]) for i in range(workers)]


def end(*_):
    for k in kids:
        try:
            k.kill()
        except OSError:
            pass
    os._exit(0)


signal.signal(signal.SIGTERM, end)
peer = {outer: inner, inner: outer}
frames = {outer: 0, inner: 0}
last = time.time()
while True:
    r, _, _ = select.select([outer, inner], [], [], 1.0)
    for fd in r:
        try:
            os.write(peer[fd], os.read(fd, 65536))
            frames[fd] += 1
        except OSError:
            pass
    if time.time() - last >= 30:
        last = time.time()
        with open("%s/relay.%s" % (D, name), "w") as f:
            f.write("%d in %d out %d\n" % (int(last), frames[outer], frames[inner]))
        if os.path.exists(D + "/stop"):
            end()
