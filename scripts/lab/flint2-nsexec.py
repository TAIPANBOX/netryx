# python3 -I nsexec.py <pid> <cmd...>: run a command in the network and mount namespaces of pid
import os, sys
p = sys.argv[1]
for ns, fl in (("net", os.CLONE_NEWNET), ("mnt", os.CLONE_NEWNS)):
    fd = os.open("/proc/%s/ns/%s" % (p, ns), os.O_RDONLY); os.setns(fd, fl); os.close(fd)
os.execvp(sys.argv[2], sys.argv[2:])
