#!/usr/bin/env bash
# fetch-librealm.sh — 获取/构建 LZSync 依赖的 realm-core native 库（linux-amd64）
#
# 产物（均落仓库 build/native/ 下）：
#   build/native/linux-amd64/librealm-ffi.so   C API 动态库（purego Dlopen 目标）
#   build/native/include/realm.h               C API umbrella 头（244 个 RLM_API）
#   build/native/include/realm/error_codes.h   realm.h 的唯一非标准库依赖
#
# 幂等语义：
#   - 产物已就位且 sha256 与下方 pin 一致 → 跳过下载/构建，仅做符号自检后退出 0
#   - 产物存在但 sha256 与 pin 不符 → 符号自检兜底：通过则警告哈希漂移后放行
#     （本地重建产物字节必然不同于 pin，属正常）；自检失败视为损坏，走重建
#   - 产物缺失 → 完整流程：下载源码 tarball（校验 sha256）→ 解压 → guix 构建 → 拷贝
#
# 版本钉死依据 docs/design.md §1.1 S5a：四端 Realm NuGet 全部 20.1.0（内置
# realm-core 20.1.1，file format v24），librealm 必须钉死 20.1.x 同代对齐，
# 绝不带更高版本 core 触碰用户库（v10+ 格式自动升级不可逆）。
#
# 构建参数说明（实测结论，勿改）：
#   1. 关测试的开关是 REALM_NO_TESTS=ON（不是 REALM_BUILD_TESTS=OFF，后者无效）
#   2. C API 所在构建 target 叫 librealm-ffi.so（不是 realm）
#   3. 必须用 gcc-toolchain@13（GCC 13.3）：系统默认 GCC 16 有 glibc isnan 宏
#      与模板体限定名解析冲突（std::isnan 展开为 std::__builtin_isnan 报 196 错，
#      -include cmath 与 -Wno-template-body 均救不了）
#   4. REALM_ENABLE_SYNC=OFF：cmake 4.1.3 会警告该变量「未被项目使用」——20.1.1
#      的 librealm-ffi 目标本就不编译 sync（object-store/sync/ 不进构建树，
#      产物无 realm_sync_*/realm_app_* 协议符号，实测 0 个）。保留此参数是为
#      对齐已实测的参数组合；LZSync 只走本地动态模式（design.md §2），无 sync 需求
#
# 其他平台（待 CI 扩展，本脚本不实现，见 docs/dev-native.md）：
#   - windows-x64     → build/native/windows-x64/realm.dll（需 MSVC，Guix 无此工具链）
#   - macos-universal → build/native/macos-universal/librealm-ffi.dylib
#                       （-DCMAKE_OSX_ARCHITECTURES="arm64;x86_64"，需 macOS 构建机，
#                        design.md §8.3 T9 待定项）

set -euo pipefail

# ── 版本与校验 pin ────────────────────────────────────────────────
REALM_VERSION="20.1.1"
TARBALL_URL="https://github.com/realm/realm-core/archive/refs/tags/v${REALM_VERSION}.tar.gz"
TARBALL_SHA256="4fd8ef3151485ba12f2c22d28ea539ca7f5c6db29de5e8d1a2e3344f1b53a954"

# 本次已验证产物（GCC 13.3 / cmake 4.1.3 Release 构建，2026-10-24 经脚本全链路
# 下载→校验→解压→构建→拷贝→自检实测）的 sha256。
# 注意：realm-core 构建不可逐字节复现（嵌入时间戳/构建路径），本地重建产物
# 哈希必然不同；此 pin 仅用于漂移检测与幂等快路径，重建后请更新此值并同步
# docs/dev-native.md 的记录。
PINNED_SO_SHA256="08e5b0025d66eebd16b286b1aad87c2df262ecbdc256b441e76f605963cf07a5"

# 核心导出符号（打开/配置/schema/事务/错误处理生命周期面）
REQUIRED_SYMBOLS=(
    realm_open
    realm_config_new
    realm_config_set_schema
    realm_get_schema
    realm_get_schema_version
    realm_begin_write
    realm_commit
    realm_rollback
    realm_get_last_error
    realm_release
)

# ── 路径 ─────────────────────────────────────────────────────────
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_LIB="$REPO_ROOT/build/native/linux-amd64/librealm-ffi.so"
OUT_HEADER="$REPO_ROOT/build/native/include/realm.h"
OUT_ERRCODES="$REPO_ROOT/build/native/include/realm/error_codes.h"

WORKDIR="${REALM_WORKDIR:-/tmp/lzsync-librealm}"   # 源码与构建缓存目录
SRC="$WORKDIR/realm-core-${REALM_VERSION}"
BUILD="$WORKDIR/build-linux-amd64"
TARBALL="$WORKDIR/realm-core-v${REALM_VERSION}.tar.gz"

# ── 工具函数 ─────────────────────────────────────────────────────
check_symbols() {
    # 对 $1 的动态库逐一核对 REQUIRED_SYMBOLS 是否以 T（导出文本段）符号存在。
    # 注意 1：双引号末尾的 $ 是正则行尾锚点（bash 对 "$ 保留字面 $ 传给 grep）。
    # 注意 2：禁用 grep -q——它在命中后早退会使 nm 收 SIGPIPE(141)，在 pipefail
    #         下管道被判失败造成假性「符号缺失」；非 -q + >/dev/null 读完整输入无此问题。
    local lib="$1" sym missing=0
    for sym in "${REQUIRED_SYMBOLS[@]}"; do
        if ! nm -D "$lib" | grep " T ${sym}$" > /dev/null; then
            echo "  缺失导出符号: $sym" >&2
            missing=1
        fi
    done
    return "$missing"
}

print_status() {
    echo "lib:     $OUT_LIB"
    sha256sum "$OUT_LIB"
    echo "header:  $OUT_HEADER"
    sha256sum "$OUT_HEADER"
    echo "header2: $OUT_ERRCODES"
    sha256sum "$OUT_ERRCODES"
}

# install_resource 把已就位的 .so 同步到 mygo 平台资源目录：
# resources/linux-amd64/ 的内容随 linux/amd64 应用打包（可执行文件同目录，
# realmc 打包路径解析命中），本地 `mygo build`/`just build` 由此产出
# 可直接分发的目录形态。
install_resource() {
    local res="$REPO_ROOT/resources/linux-amd64/librealm-ffi.so"
    mkdir -p "$(dirname "$res")"
    cp "$OUT_LIB" "$res"
    echo "[resource] $res"
}

# ── 幂等快路径：产物已就位 ───────────────────────────────────────
if [ -f "$OUT_LIB" ] && [ -f "$OUT_HEADER" ] && [ -f "$OUT_ERRCODES" ]; then
    actual_sha="$(sha256sum "$OUT_LIB" | cut -d' ' -f1)"
    if [ "$actual_sha" = "$PINNED_SO_SHA256" ]; then
        if check_symbols "$OUT_LIB"; then
            echo "[skip] librealm-ffi.so 已就位，sha256 与 pin 一致且符号自检通过，跳过获取/构建"
            echo "[done] 产物状态："
            print_status
            install_resource
            exit 0
        fi
        echo "[fail] 产物 sha256 与 pin 一致但符号自检未过（异常态），转入重建流程"
    else
        echo "[warn] 产物 sha256 与 pin 不符："
        echo "  pin:    $PINNED_SO_SHA256"
        echo "  actual: $actual_sha"
        if check_symbols "$OUT_LIB"; then
            echo "[warn] 符号自检通过（本地重建产物哈希漂移属正常），放行；"
            echo "       如需固化新产物，请更新脚本 PINNED_SO_SHA256 与 docs/dev-native.md"
            echo "[done] 产物状态："
            print_status
            install_resource
            exit 0
        fi
        echo "[fail] 符号自检未过，产物疑似损坏，转入重建流程"
    fi
fi

# ── 获取源码（自动判断是否需要重新下载）──────────────────────────
mkdir -p "$WORKDIR"

if [ -f "$SRC/src/realm.h" ]; then
    echo "[skip] 源码已存在: $SRC"
else
    if [ -f "$TARBALL" ] && echo "${TARBALL_SHA256}  ${TARBALL}" | sha256sum -c --status; then
        echo "[skip] tarball 已存在且校验通过: $TARBALL"
    else
        echo "[fetch] 下载 $TARBALL_URL"
        curl -fSL --retry 3 -o "$TARBALL" "$TARBALL_URL"
    fi
    echo "[verify] tarball sha256 校验"
    echo "${TARBALL_SHA256}  ${TARBALL}" | sha256sum -c
    echo "[extract] 解压到 $WORKDIR"
    tar -xzf "$TARBALL" -C "$WORKDIR"
    test -f "$SRC/src/realm.h"
fi

# ── 构建（Guix 一次性环境，参数见文件头注释，勿改）──────────────
echo "[build] configure + build librealm-ffi.so (GCC 13.3, Release, sync=OFF, no tests)"
guix shell cmake ninja gcc-toolchain@13 -- env \
    REALM_SRC="$SRC" REALM_BUILD="$BUILD" sh -c '
        set -eu
        cmake -S "$REALM_SRC" -B "$REALM_BUILD" -GNinja \
            -DCMAKE_BUILD_TYPE=Release \
            -DREALM_ENABLE_SYNC=OFF \
            -DREALM_NO_TESTS=ON
        cmake --build "$REALM_BUILD" --target librealm-ffi.so
    '

BUILT_LIB="$BUILD/src/realm/object-store/c_api/librealm-ffi.so"
test -f "$BUILT_LIB"

# ── 拷贝产物 ─────────────────────────────────────────────────────
echo "[install] 拷贝产物到 build/native/"
mkdir -p "$(dirname "$OUT_LIB")" "$(dirname "$OUT_ERRCODES")"
cp "$BUILT_LIB" "$OUT_LIB"
cp "$SRC/src/realm.h" "$OUT_HEADER"
cp "$SRC/src/realm/error_codes.h" "$OUT_ERRCODES"

# ── 结尾自检：符号 + sha256 ──────────────────────────────────────
echo "[self-check] 导出符号自检："
if check_symbols "$OUT_LIB"; then
    echo "  全部命中: ${REQUIRED_SYMBOLS[*]}"
else
    echo "[fail] 导出符号自检未通过（详见上方缺失清单），产物不视为就位" >&2
    exit 1
fi

echo "[done] 产物状态："
print_status
install_resource
echo "若 sha256 与脚本 PINNED_SO_SHA256 / docs/dev-native.md 记录不一致，请同步更新两处。"
