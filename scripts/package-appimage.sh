#!/usr/bin/env bash
# package-appimage.sh — 由 mygo build 的 linux-amd64 产物装配 AppImage。
#
# 用法：scripts/package-appimage.sh [版本]
# 前置：build/linux-amd64/ 已有 lzsync、librealm-ffi.so、lzsync.desktop、
# lzsync.png。产物：build/linux-amd64/LZSync-<版本>-x86_64.AppImage
#
# AppDir 布局：AppRun → usr/bin/lzsync，realm-core 库与可执行文件同目录
# （realmc 打包路径解析命中）。注意：AppImage 不内嵌 WebKitGTK——运行依赖
# 发行版安装的 webkit2gtk-4.1/4.0（与 .deb/.rpm 的 Depends 同级语义），
# 缺库时 mygo 会给出明确安装提示。
#
# appimagetool 官方发布物是滚动 continuous 标签的二进制，此处按文件名缓存
# 于 build/appimage/（APPIMAGETOOL 环境变量可指向本地副本跳过下载）。

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STAGE="$REPO_ROOT/build/linux-amd64"
VERSION="${1:-$(sed -n 's/.*version: *"\([^"]*\)".*/\1/p' "$REPO_ROOT/mygo.config.ts" | head -1)}"
TOOLS_DIR="$REPO_ROOT/build/appimage"
APP_DIR="$TOOLS_DIR/LZSync.AppDir"
OUT="$STAGE/LZSync-$VERSION-x86_64.AppImage"
TOOL="${APPIMAGETOOL:-$TOOLS_DIR/appimagetool-x86_64.AppImage}"
TOOL_URL="https://github.com/AppImage/appimagetool/releases/download/continuous/appimagetool-x86_64.AppImage"

for f in lzsync librealm-ffi.so lzsync.desktop lzsync.png; do
	[ -f "$STAGE/$f" ] || {
		echo "[fail] 缺少产物 $STAGE/$f（先跑 mygo build 与 librealm 装配）" >&2
		exit 1
	}
done

mkdir -p "$TOOLS_DIR"
if [ ! -x "$TOOL" ]; then
	echo "[fetch] $TOOL_URL"
	curl -fSL --retry 3 -o "$TOOL" "$TOOL_URL"
	chmod +x "$TOOL"
fi

rm -rf "$APP_DIR"
mkdir -p "$APP_DIR/usr/bin"
cp "$STAGE/lzsync" "$STAGE/librealm-ffi.so" "$APP_DIR/usr/bin/"
chmod 755 "$APP_DIR/usr/bin/lzsync"

cat > "$APP_DIR/AppRun" <<'EOF'
#!/bin/sh
HERE="$(dirname "$(readlink -f "$0")")"
exec "$HERE/usr/bin/lzsync" "$@"
EOF
chmod 755 "$APP_DIR/AppRun"

# AppDir 根：desktop 与图标按 appimagetool 约定（Exec 名与 Icon 名一致）
sed 's|^Exec=.*|Exec=lzsync|' "$STAGE/lzsync.desktop" > "$APP_DIR/lzsync.desktop"
cp "$STAGE/lzsync.png" "$APP_DIR/lzsync.png"

rm -f "$OUT"
ARCH=x86_64 "$TOOL" --appimage-extract-and-run "$APP_DIR" "$OUT"
echo "[done] $OUT"
