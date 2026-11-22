#!/bin/sh
# Runs on forge, in the tree scripts/forge.sh copies there, after
# package/netryx/build.sh:
#   scripts/forge.sh sh package/netryx/build.sh aarch64_cortex-a53
#   scripts/forge.sh sh scripts/lab/forge-mkapk.sh aarch64_cortex-a53
# The same `apk mkpkg` as package/netryx/mkapk.sh (its scripts, depends and
# description are read from that file), run inside an ephemeral incus Alpine
# edge container instead of docker: forge's lab user has incus and no docker
# socket. Files in the package are owned by root, as the container's root
# packs them. Output: build/<arch>/netryx-<version>-r<rel>.apk.
set -eu
ARCH=${1:-aarch64_cortex-a53}
VERSION=${VERSION:-0.1.0}
PKGREL=${PKGREL:-1}
WORK=build/$ARCH
OUT=netryx-$VERSION-r$PKGREL.apk
heredoc() { awk -v start="cat > \"\$$2\" <<'$3'" -v end="$3" '$0 == start { on = 1; next } on && $0 == end { on = 0 } on' "$1"; }
heredoc package/netryx/mkapk.sh WORK/post-install POST >"$WORK/post-install"
heredoc package/netryx/mkapk.sh WORK/pre-deinstall PRE >"$WORK/pre-deinstall"
heredoc package/netryx/mkapk.sh WORK/post-deinstall POSTDEL >"$WORK/post-deinstall"
chmod 0755 "$WORK/post-install" "$WORK/pre-deinstall" "$WORK/post-deinstall"
find "$WORK/tree" -name .DS_Store -delete
rm -rf "$WORK/tree/CONTROL"
depends=$(sed -n 's/.*--info "depends:\([^"]*\)".*/\1/p' package/netryx/mkapk.sh)
desc=$(sed -n 's/.*--info "description:\([^"]*\)".*/\1/p' package/netryx/mkapk.sh)
C=nx-mkapk-$$
incus launch images:alpine/edge "$C" --ephemeral >/dev/null
trap 'incus stop "$C" --force >/dev/null 2>&1 || true' EXIT
# wait for the container's init
i=0; until incus exec "$C" -- true 2>/dev/null || [ $i -ge 30 ]; do sleep 1; i=$((i+1)); done
incus exec "$C" -- mkdir -p /work
tar -C "$WORK" -cf - tree post-install pre-deinstall post-deinstall | incus exec "$C" -- tar -C /work --no-same-owner -xf -
incus exec "$C" -- chown -R 0:0 /work
incus exec "$C" -- apk --version
incus exec "$C" -- sh -c "cd /work && apk mkpkg \
	--info 'name:netryx' \
	--info 'version:$VERSION-r$PKGREL' \
	--info 'arch:$ARCH' \
	--info 'license:MIT' \
	--info 'origin:netryx' \
	--info 'url:https://github.com/TAIPANBOX/netryx' \
	--info 'description:$desc' \
	--info 'depends:$depends' \
	--script 'post-install:/work/post-install' \
	--script 'post-upgrade:/work/post-install' \
	--script 'pre-deinstall:/work/pre-deinstall' \
	--script 'post-deinstall:/work/post-deinstall' \
	--files /work/tree \
	--output /work/$OUT"
incus file pull "$C/work/$OUT" "$WORK/$OUT"
ls -l "$WORK/$OUT"
sha256sum "$WORK/$OUT"
