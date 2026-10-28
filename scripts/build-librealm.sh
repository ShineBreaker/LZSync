#!/usr/bin/env bash
# build-librealm.sh — CI 跨平台构建 LZSync 依赖的 realm-core C API 库。
#
# 用法：scripts/build-librealm.sh <target>
#   linux-amd64      → build/native/linux-amd64/librealm-ffi.so
#                        + resources/linux-amd64/librealm-ffi.so
#   windows-x64      → build/native/windows-x64/realm-ffi.dll
#                        + resources/windows-amd64/realm-ffi.dll
#   macos-universal  → build/native/macos-universal/librealm-ffi.dylib
#                        + resources/darwin/librealm-ffi.dylib
#
# resources/<平台>/ 是 mygo build 的平台资源目录（resources/darwin 对所有
# darwin 目标生效，universal dylib 直接整包随 .app 进 Contents/Resources）。
#
# 幂等：build/native/<target>/<lib> 已存在且非空即跳过编译（CI 用
# actions/cache 缓存该目录）；FORCE=1 强制重建。
#
# 版本钉死 20.1.1 与 scripts/fetch-librealm.sh 同源（design.md §1.1 S5a：
# 对齐四端 Realm NuGet 20.1.0 / file format v24），构建参数与已实测的
# linux 参数一致（REALM_NO_TESTS=ON、sync 关闭）。Windows 需 MSVC
# （windows-latest 自带 VS，CMake 自动选最高版本生成器），macOS 需 Xcode
# CLT（macos-latest 自带）；Linux 优先用 gcc-13/g++-13（与已验证产物
# 同代，缺省回退 cc/c++）。
#
# 本地开发只构建 linux-amd64 请走 fetch-librealm.sh（guix 环境）。

set -euo pipefail

TARGET="${1:?用法: build-librealm.sh <linux-amd64|windows-x64|macos-universal>}"

REALM_VERSION="20.1.1"
TARBALL_URL="https://github.com/realm/realm-core/archive/refs/tags/v${REALM_VERSION}.tar.gz"
TARBALL_SHA256="4fd8ef3151485ba12f2c22d28ea539ca7f5c6db29de5e8d1a2e3344f1b53a954"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKDIR="${REALM_WORKDIR:-/tmp/lzsync-librealm-ci}"
SRC="$WORKDIR/realm-core-${REALM_VERSION}"
BUILD="$WORKDIR/build-$TARGET"
TARBALL="$WORKDIR/realm-core-v${REALM_VERSION}.tar.gz"

case "$TARGET" in
linux-amd64)
	LIB_NAME="librealm-ffi.so"
	LIB_SUBPATH="librealm-ffi.so"
	RES_DIR="linux-amd64"
	;;
windows-x64)
	LIB_NAME="realm-ffi.dll"
	LIB_SUBPATH="Release/realm-ffi.dll" # VS 生成器多配置布局
	RES_DIR="windows-amd64"
	;;
macos-universal)
	LIB_NAME="librealm-ffi.dylib"
	LIB_SUBPATH="librealm-ffi.dylib"
	RES_DIR="darwin" # resources/darwin 对所有 darwin 目标生效
	;;
*)
	echo "[fail] 未知 target: $TARGET" >&2
	exit 2
	;;
esac

OUT_LIB="$REPO_ROOT/build/native/$TARGET/$LIB_NAME"
OUT_RES="$REPO_ROOT/resources/$RES_DIR/$LIB_NAME"

install_lib() {
	# $1 = 构建产物路径；同步到 build/native/ 与 resources/ 两处
	mkdir -p "$(dirname "$OUT_LIB")" "$(dirname "$OUT_RES")" \
		"$REPO_ROOT/build/native/include/realm"
	[ "$1" = "$OUT_LIB" ] || cp "$1" "$OUT_LIB"
	cp "$1" "$OUT_RES"
	if [ -f "$SRC/src/realm.h" ]; then
		cp "$SRC/src/realm.h" "$REPO_ROOT/build/native/include/realm.h"
		cp "$SRC/src/realm/error_codes.h" "$REPO_ROOT/build/native/include/realm/error_codes.h"
	fi
	echo "[done] 产物："
	ls -lh "$OUT_LIB" "$OUT_RES"
}

# ── 幂等快路径 ──────────────────────────────────────────────────
if [ "${FORCE:-}" != "1" ] && [ -f "$OUT_LIB" ] && [ "$(stat -c%s "$OUT_LIB" 2>/dev/null || stat -f%z "$OUT_LIB")" -gt 1000000 ]; then
	echo "[skip] $OUT_LIB 已存在（>1MB），跳过构建；FORCE=1 可强制重建"
	install_lib "$OUT_LIB" # 同步 resources/（CI cache 只恢复 build/native）
	exit 0
fi

# ── 获取源码 ────────────────────────────────────────────────────
# 校验用「算哈希 + 字符串比较」，不走 sha256sum -c：macOS 自带的是 BSD 变体
# （usage: sha256sum [-bctwz]，无 -c/--status），git-bash 的才是 GNU coreutils。
sha256of() {
	case "$(uname -s)" in
	Darwin) shasum -a 256 "$1" | cut -d' ' -f1 ;;
	*) sha256sum "$1" | cut -d' ' -f1 ;;
	esac
}

mkdir -p "$WORKDIR"
if [ -f "$SRC/src/realm.h" ]; then
	echo "[skip] 源码已存在: $SRC"
else
	if [ -f "$TARBALL" ] && [ "$(sha256of "$TARBALL")" = "$TARBALL_SHA256" ]; then
		echo "[skip] tarball 已存在且校验通过"
	else
		echo "[fetch] 下载 $TARBALL_URL"
		curl -fSL --retry 3 -o "$TARBALL" "$TARBALL_URL"
	fi
	[ "$(sha256of "$TARBALL")" = "$TARBALL_SHA256" ] || {
		echo "[fail] tarball sha256 不符: $(sha256of "$TARBALL")" >&2
		exit 1
	}
	tar -xzf "$TARBALL" -C "$WORKDIR"
fi

# GitHub 源码 tarball 不含 submodule 内容。Windows 无 OpenSSL 时 realm 回退
# 内置 sha-1/sha-2（gitlink 钉定 commit 如下），缺文件会报
# "Cannot find source file: ../external/sha-2/sha224.cpp"；test/catch 用不上
# （REALM_NO_TESTS=ON）。commit 经 git ls-tree v20.1.1 src/external 确认。
fetch_sub() {
	# $1=GitHub repo（org/name）$2=commit sha $3=目标目录
	if [ -n "$(ls -A "$3" 2>/dev/null)" ]; then
		echo "[skip] submodule $1 已就位"
		return
	fi
	mkdir -p "$3"
	echo "[fetch] submodule $1@$2"
	curl -fSL --retry 3 "https://github.com/$1/archive/$2.tar.gz" |
		tar -xz --strip-components=1 -C "$3"
}
fetch_sub clibs/sha1 d9ae30f34095107ece9dceb224839f0dc2f9c1c7 "$SRC/src/external/sha-1"
fetch_sub kalven/sha-2 0e9aebf34101c6aa89355fd76ac9cd886735dee1 "$SRC/src/external/sha-2"

# ── 构建 ────────────────────────────────────────────────────────
case "$TARGET" in
linux-amd64)
	# GCC 13.3 与已验证产物同代（GCC 16 有 glibc isnan 宏冲突，见
	# fetch-librealm.sh 头注）；runner 上无 gcc-13 时回退系统 cc。
	if command -v gcc-13 >/dev/null 2>&1; then
		export CC=gcc-13 CXX=g++-13
	fi
	GEN="Unix Makefiles"
	command -v ninja >/dev/null 2>&1 && GEN="Ninja"
	cmake -S "$SRC" -B "$BUILD" -G "$GEN" \
		-DCMAKE_BUILD_TYPE=Release \
		-DREALM_ENABLE_SYNC=OFF \
		-DREALM_NO_TESTS=ON
	cmake --build "$BUILD" --target RealmFFI --parallel "$(nproc)"
	;;
windows-x64)
	# 不钉死 -G：windows-latest 已迁 windows-2025/VS2026，无 -G 时
	# CMake 经 vswhere 自动选最高版本 VS；多配置布局（Release/）不变。
	cmake -S "$SRC" -B "$BUILD" -A x64 \
		-DREALM_ENABLE_SYNC=OFF \
		-DREALM_NO_TESTS=ON
	cmake --build "$BUILD" --target RealmFFI --config Release
	;;
macos-universal)
	# vendored s2geometry 特化 std::is_pod——新版 libc++（Xcode 16+）对
	# 该 trait 加了「禁止特化」的默认报错诊断（-Winvalid-specialization，
	# ENABLE_REMOVED_TYPE_TRAITS 宏无法解除），用 -Wno- 降级为静默即可编译；
	# 另新版 libc++ 收紧传递包含（cli_args.cpp 用 std::strtol 未引 <cstdlib>
	# 直接报错），-include cstdlib 对全 TU 兜底此类缺失
	cmake -S "$SRC" -B "$BUILD" \
		-DCMAKE_BUILD_TYPE=Release \
		-DCMAKE_OSX_ARCHITECTURES="arm64;x86_64" \
		-DCMAKE_OSX_DEPLOYMENT_TARGET=12.0 \
		"-DCMAKE_CXX_FLAGS=-Wno-invalid-specialization -include cstdlib -D_LIBCPP_ENABLE_CXX20_REMOVED_TYPE_TRAITS -D_LIBCPP_ENABLE_CXX26_REMOVED_TYPE_TRAITS" \
		-DREALM_ENABLE_SYNC=OFF \
		-DREALM_NO_TESTS=ON
	cmake --build "$BUILD" --target RealmFFI --parallel "$(sysctl -n hw.ncpu)"
	;;
esac

BUILT_LIB="$BUILD/src/realm/object-store/c_api/$LIB_SUBPATH"
[ -f "$BUILT_LIB" ] || {
	echo "[fail] 未找到构建产物: $BUILT_LIB" >&2
	exit 1
}

# ── 自检 ────────────────────────────────────────────────────────
case "$LIB_NAME" in
*.so)
	# ELF：nm -D 读动态符号（勿用 grep -q：SIGPIPE 在 pipefail 下假报缺失）
	for sym in realm_open realm_config_new realm_commit realm_get_last_error realm_release; do
		nm -D "$BUILT_LIB" 2>/dev/null | grep " T ${sym}$" >/dev/null ||
			{
				echo "[fail] 缺失导出符号: $sym" >&2
				exit 1
			}
	done
	echo "[check] 导出符号自检通过"
	;;
*.dylib)
	# Mach-O：nm -gU 列出全局定义符号，C 符号带 _ 前缀
	for sym in realm_open realm_config_new realm_commit realm_get_last_error realm_release; do
		nm -gU "$BUILT_LIB" 2>/dev/null | grep " T _${sym}$" >/dev/null ||
			{
				echo "[fail] 缺失导出符号: $sym" >&2
				exit 1
			}
	done
	echo "[check] 导出符号自检通过"
	;;
*.dll)
	# git-bash 无现成符号工具；产物非空即通过（链接成功即符号齐备）
	SIZE=$(stat -c%s "$BUILT_LIB" 2>/dev/null || stat -f%z "$BUILT_LIB")
	[ "$SIZE" -gt 1000000 ] || {
		echo "[fail] realm-ffi.dll 异常小: $SIZE" >&2
		exit 1
	}
	echo "[check] realm-ffi.dll 产物大小正常 ($SIZE bytes)"
	;;
esac

install_lib "$BUILT_LIB"
