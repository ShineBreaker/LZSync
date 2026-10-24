# 读写游戏侧 realm 一律走 purego + realm-core C API 动态模式

读与写 fork 侧 .realm 只有一条路：purego Dlopen librealm-ffi.so（自建 realm-core 20.1.1，版本钉死与四端 Realm NuGet 20.1.0 对齐），以动态模式（不传 schema）打开，钉死磁盘版本、只写目标库已有列、事务写入 + 结构漂移校验。这是 EZRealmSync 用 Realm.Net 在生产验证过的手法在 Go 侧的复刻。

## Considered Options

- 纯 Go 解析 .realm 二进制：官方明确格式不供外部程序操作（realm-core issue #3691），公开解析器全部停在 file format v9，而 lazer 系在 v24——实际不可行。
- .NET 子进程助手：realm-dotnet 主仓库停摆一年余、CI 损坏，且 NativeAOT 不支持工具必需的 Dynamic API。
- 完全不碰 realm：见 ADR-0001 降级路径。

## Consequences

- 分发形态是"产物目录 = Go 二进制 + librealm 共享库"，不做严格单二进制；native 库缺失时应用降级启动（只诊断不可写）。
- Linux 构建必须注入纯 Go DummyScheduler（realm-core 20.1.1 无平台默认 scheduler 时 realm_open 直接终止进程）；C 结构体的字符串字段必须空串填充（NULL 会段错误）；ABI 布局以 Sizeof/Offsetof 断言逐字段对齐 realm.h。三处均为 PoC 实证，已固化在 internal/realmc 测试。
- librealm 构建必须 gcc-toolchain@13（GCC 16 与 glibc isnan 宏冲突），见 scripts/fetch-librealm.sh 头注的完整避坑清单。
- realm 文件格式 v10+ 自动升级不可逆：若某 fork 未来升级 Realm SDK，旧 LZSync 拒绝读写其库并提示升级，绝不带高版本 core 触碰用户库。
