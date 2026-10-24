# native 库（realm-core）开发手册

> LZSync 的 Realm 读写经 purego Dlopen realm-core C API 动态库（design.md §2 选型）。
> 本文记录该 native 库的版本钉死策略、获取/重建方式、符号自检方法与产物指纹。
> 获取入口统一为 `scripts/fetch-librealm.sh`，勿手工散落构建。

## 1. 版本钉死策略

**librealm 钉死 realm-core 20.1.x（当前 20.1.1），绝不浮动**。依据 `docs/design.md` §1.1 S5a（file format 版本防线）：

- 四端 Realm NuGet 全部 20.1.0（`reference/{lazer,delta,Ez2Lazer,g0v0}/osu.Game/osu.Game.csproj` 核验，内置 core 20.1.1，file format v24）；
- realm 文件格式 v10+ 的自动升级**不可逆**（旧 SDK 无法再打开），携带高版本 core 触碰用户库等于替用户做不可逆决定；
- 对打开失败的库（文件格式比 20.1.x 更新，未来某端升级 SDK 后的场景）一律拒绝读写并提示升级 LZSync（S5a 原文）。

版本变更纪律：任何 bump 必须先核验四端客户端的 Realm NuGet 版本同步对齐，并更新 `scripts/fetch-librealm.sh` 中的 `REALM_VERSION`、`TARBALL_SHA256`、`PINNED_SO_SHA256` 与本文 §4 的指纹记录。

## 2. 产物清单（linux-amd64）

| 产物 | 路径 | 说明 |
|------|------|------|
| 动态库 | `build/native/linux-amd64/librealm-ffi.so` | purego Dlopen 目标；ELF x86-64，GCC 13.3 Release 构建，not stripped，约 13.7 MB |
| C API 头 | `build/native/include/realm.h` | umbrella 头，244 个 `RLM_API` |
| 错误码头 | `build/native/include/realm/error_codes.h` | realm.h 的唯一非标准库依赖（`#include <realm/error_codes.h>`），纯枚举无传递依赖 |

realm.h 其余 include 全部是标准头（stddef/stdint/stdbool），Go 侧 FFI 绑定仅将其作参考（不参与编译），C 工具链仅在需要本地编译辅助工具时用到。

## 3. 获取与重建

```sh
./scripts/fetch-librealm.sh          # 幂等：产物就位且校验通过则跳过，缺失/损坏则全流程重建
```

脚本行为（各分支均已实测）：

- 产物存在且 sha256 == pin → 跳过下载/构建，仅符号自检 + 打印指纹，退出 0；
- 产物存在但 sha256 != pin → 符号自检兜底：通过则警告哈希漂移后放行（**realm-core 构建不可逐字节复现**，本地重建产物哈希必然不同）；自检失败视为损坏走重建；
- 产物缺失 → 下载源码 tarball（`v20.1.1.tar.gz`，sha256 校验）→ 解压 → Guix 一次性环境构建 → 拷贝产物 → 结尾符号自检 + 打印 sha256。

构建环境（一次性 shell，不污染 profile，符合仓库 AGENTS.md 的 direnv/Guix 约定）：

```sh
guix shell cmake ninja gcc-toolchain@13 -- sh -c '
    cmake -S <src> -B <build> -GNinja \
        -DCMAKE_BUILD_TYPE=Release \
        -DREALM_ENABLE_SYNC=OFF \
        -DREALM_NO_TESTS=ON && \
    cmake --build <build> --target librealm-ffi.so
'
# 产物：<build>/src/realm/object-store/c_api/librealm-ffi.so
```

构建参数的实测结论（勿改）：

1. **`REALM_NO_TESTS=ON`** 才能关测试（`REALM_BUILD_TESTS=OFF` 无效）；
2. C API target 叫 **`librealm-ffi.so`**（不是 `realm`）；
3. **必须 `gcc-toolchain@13`（GCC 13.3）**：系统默认 GCC 16 有 glibc `isnan` 宏与模板体限定名解析冲突（`std::isnan` 展开为 `std::__builtin_isnan` 报 196 个错，`-include cmath` 与 `-Wno-template-body` 均救不了）；
4. `REALM_ENABLE_SYNC=OFF` 会被 cmake 4.1.3 警告「未被项目使用」——20.1.1 的 librealm-ffi 目标本就不编译 sync（`object-store/sync/` 不进构建树，产物无 `realm_sync_*`/`realm_app_*` 协议符号，实测 0 个）。保留该参数是为对齐已实测参数组合；LZSync 只走本地动态模式，无 sync 需求。

源码与构建缓存放 `/tmp/lzsync-librealm`（可用 `REALM_WORKDIR` 覆盖），重复运行自动跳过已下载源码与已编译目标（ninja 增量）。

## 4. 产物指纹记录（2026-10-24，经脚本全链路实测构建）

```
8387f82b8239dfbd97ca1529ac55d003b2481d553d584e36f187e5d160117502  librealm-ffi.so   # ①主会话同参数构建，已被下述重建取代
08e5b0025d66eebd16b286b1aad87c2df262ecbdc256b441e76f605963cf07a5  librealm-ffi.so   # ②当前 pin：脚本全链路（下载→校验→构建→拷贝→自检）实测产物
d0e110662ef61c3a0737b7ef3cef46977e01bbba3440d6affa2c81b84cbb1b6d  realm.h
d1faf29dd9650044ea2e4d620875ca71a42663dbace3b79644806424a932b5f7  realm/error_codes.h
4fd8ef3151485ba12f2c22d28ea539ca7f5c6db29de5e8d1a2e3344f1b53a954  v20.1.1.tar.gz（源码 tarball，sha 稳定可复验）
```

构建环境：Guix `gcc-toolchain@13`（GCC 13.3.0）+ cmake 4.1.3 + ninja 1.13.2，Release，188 个编译目标。`realm.h` / `error_codes.h` 来自源码 tarball，内容确定性由 tarball sha 保证。

## 5. 符号自检方法

```sh
# 单符号验证（注意勿用 grep -q：早退触发 nm SIGPIPE，在 set -o pipefail 下假报缺失）
nm -D build/native/linux-amd64/librealm-ffi.so | grep " T realm_open$"

# 脚本内置自检面（结尾自动执行，亦可手动重跑脚本触发）
realm_open  realm_config_new  realm_config_set_schema  realm_get_schema
realm_get_schema_version  realm_begin_write  realm_commit  realm_rollback
realm_get_last_error  realm_release
```

当前产物实测：上述符号全部命中，动态导出 T 符号共 **6238** 个。补充验证过的关键面：

- **schema 构造面**（design.md §2 PoC 第二道验收线的先决证据）：`realm_schema_new`、`realm_config_set_schema`、`realm_config_set_schema_version`、`realm_config_set_schema_mode`、`realm_config_set_schema_subset_mode` 均导出；
- 事务生命周期：`realm_begin_write` / `realm_commit` / `realm_rollback`；
- 动态查询/列表面（M2 写路径需要）：`realm_results_*`（get/filter/sort/find/delete_all 等）、`realm_list_*`（insert/set/get_list/insert_embedded 等）全套；
- 注意 API 命名：**v20 无 `realm_open_local`**（统一为 `realm_open`）、事务取消是 **`realm_rollback`**（无 `realm_cancel_transaction`）、类查找是 `realm_find_class`/`realm_find_property`（无 `realm_find`）——绑定层命名以此为准，勿按旧版 SDK 想当然。

## 6. 其他平台（待 CI 扩展，本仓库暂不实现）

- **windows-x64** → `build/native/windows-x64/realm.dll`：需 MSVC 工具链（Guix 无），规划走 GitHub Actions Windows runner；
- **macos-universal** → `build/native/macos-universal/librealm-ffi.dylib`：`-DCMAKE_OSX_ARCHITECTURES="arm64;x86_64"`，需 macOS 构建机（成本待定项见 design.md §8.3 T9）；
- 三平台目标与产物目录形态见 design.md §1.3 P-1、CI 流水线规划见 §3.1 `tools/build-librealm`；两平台就位前，M1 的「linux-amd64 实机 Dlopen 验证」先行。
