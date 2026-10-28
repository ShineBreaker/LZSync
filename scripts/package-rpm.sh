#!/usr/bin/env bash
# package-rpm.sh — 由 mygo build 的 linux-amd64 产物装配 RPM 包。
#
# 用法：scripts/package-rpm.sh [版本]
# 前置：build/linux-amd64/ 已有 lzsync、librealm-ffi.so、lzsync.desktop、
# lzsync.png、icon.png（即 resources/linux-amd64 已就位后的 mygo build）。
# 产物：build/linux-amd64/lzsync-<版本>-1.x86_64.rpm
#
# 需要 rpmbuild（CI: apt install rpm）。构建树放 build/rpm/。

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STAGE="$REPO_ROOT/build/linux-amd64"
VERSION="${1:-$(sed -n 's/.*version: *"\([^"]*\)".*/\1/p' "$REPO_ROOT/mygo.config.ts" | head -1)}"
TOP="$REPO_ROOT/build/rpm"
PAYLOAD="$TOP/BUILD/lzsync-payload"

for f in lzsync librealm-ffi.so lzsync.desktop lzsync.png icon.png; do
	[ -f "$STAGE/$f" ] || {
		echo "[fail] 缺少产物 $STAGE/$f（先跑 mygo build 与 librealm 装配）" >&2
		exit 1
	}
done

rm -rf "$PAYLOAD"
mkdir -p "$PAYLOAD/opt/lzsync" "$PAYLOAD/usr/bin" \
	"$PAYLOAD/usr/share/applications" \
	"$PAYLOAD/usr/share/icons/hicolor/1024x1024/apps"

cp "$STAGE/lzsync" "$STAGE/librealm-ffi.so" "$STAGE/icon.png" "$PAYLOAD/opt/lzsync/"
chmod 755 "$PAYLOAD/opt/lzsync/lzsync"
ln -sfn ../../opt/lzsync/lzsync "$PAYLOAD/usr/bin/lzsync"
# 系统级安装：desktop 入口用绝对路径执行（开发态 Exec=lzsync 依赖 PATH）
sed 's|^Exec=.*|Exec=/opt/lzsync/lzsync|' "$STAGE/lzsync.desktop" \
	> "$PAYLOAD/usr/share/applications/lzsync.desktop"
cp "$STAGE/lzsync.png" "$PAYLOAD/usr/share/icons/hicolor/1024x1024/apps/lzsync.png"

mkdir -p "$TOP/RPMS" "$TOP/SRPMS" "$TOP/SPECS" "$TOP/SOURCES"
rpmbuild -bb \
	--define "_topdir $TOP" \
	--define "lzsync_version $VERSION" \
	"$REPO_ROOT/packaging/lzsync.spec"

cp "$TOP"/RPMS/x86_64/lzsync-"$VERSION"-*.x86_64.rpm "$STAGE/"
echo "[done] $(ls "$STAGE"/lzsync-"$VERSION"-*.x86_64.rpm)"
