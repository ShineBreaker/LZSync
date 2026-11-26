#!/usr/bin/env bash
# package-rpm.sh — 由 mygo build 的 linux-amd64 产物装配 RPM 包。
#
# 用法：scripts/package-rpm.sh [版本]
# 前置：build/linux-amd64/ 已有 lzsync、librealm-ffi.so、lzsync.desktop、
# icon.png（即 resources/linux-amd64 已就位后的 mygo build），且系统有
# ImageMagick 的 magick 或 convert。
# 产物：build/linux-amd64/lzsync-<版本>-1.x86_64.rpm
#
# 需要 rpmbuild（CI: apt install rpm）。构建树放 build/rpm/。

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STAGE="$REPO_ROOT/build/linux-amd64"
VERSION="${1:-$(sed -n 's/.*version: *"\([^"]*\)".*/\1/p' "$REPO_ROOT/mygo.config.ts" | head -1)}"
TOP="$REPO_ROOT/build/rpm"
PAYLOAD="$TOP/SOURCES/lzsync-payload"

for f in lzsync librealm-ffi.so lzsync.desktop icon.png; do
	[ -f "$STAGE/$f" ] || {
		echo "[fail] 缺少产物 $STAGE/$f（先跑 mygo build 与 librealm 装配）" >&2
		exit 1
	}
done

ICON_CONVERT="$(command -v magick || command -v convert || true)"
[ -n "$ICON_CONVERT" ] || {
	echo "[fail] 缺少 ImageMagick（magick 或 convert），无法生成 hicolor 图标" >&2
	exit 1
}

mkdir -p "$TOP/RPMS" "$TOP/SRPMS" "$TOP/SPECS" "$TOP/SOURCES"
rm -rf "$PAYLOAD"
mkdir -p "$PAYLOAD/opt/lzsync" "$PAYLOAD/usr/bin" \
	"$PAYLOAD/usr/share/applications"

cp "$STAGE/lzsync" "$STAGE/librealm-ffi.so" "$STAGE/icon.png" "$PAYLOAD/opt/lzsync/"
chmod 755 "$PAYLOAD/opt/lzsync/lzsync"
ln -sfn ../../opt/lzsync/lzsync "$PAYLOAD/usr/bin/lzsync"
# 系统级安装：desktop 入口用绝对路径执行（开发态 Exec=lzsync 依赖 PATH）
sed 's|^Exec=.*|Exec=/opt/lzsync/lzsync|' "$STAGE/lzsync.desktop" \
	> "$PAYLOAD/usr/share/applications/lzsync.desktop"
# hicolor 索引只登记到 512x512；1024 目录会被主题查找忽略。
for px in 256 512; do
	icon_dir="$PAYLOAD/usr/share/icons/hicolor/${px}x${px}/apps"
	mkdir -p "$icon_dir"
	"$ICON_CONVERT" "$STAGE/icon.png" -resize "${px}x${px}" -strip \
		"$icon_dir/lzsync.png"
done

rpmbuild -bb \
	--define "_topdir $TOP" \
	--define "lzsync_version $VERSION" \
	"$REPO_ROOT/packaging/lzsync.spec"

cp "$TOP"/RPMS/x86_64/lzsync-"$VERSION"-*.x86_64.rpm "$STAGE/"
echo "[done] $(ls "$STAGE"/lzsync-"$VERSION"-*.x86_64.rpm)"
