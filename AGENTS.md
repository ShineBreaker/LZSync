# AGENTS.md

> LZSync 工作区指引（ZCode 子智能体注入会读到本文件，保持精简）

## 概述

LZSync：管理同一台机器上多个 osu!lazer fork（官方 lazer、DeltaLazer、Ez2Lazer、g0v0）数据的同步工具。**hub-and-spoke 架构**：SQLite 中枢为单一真相源，游戏侧 realm 库只是投影/挂载点——收集（fork→hub）与投放（hub→fork）两个方向，实体文件入自有内容寻址仓、游戏侧以链接方式持有。Go 后端 + mygo Web UI。

## 目录

| 路径 | 内容 |
|---|---|
| `internal/realmc` | purego→realm-core C API 纯绑定层（不 import 任何领域包） |
| `internal/forks` | 四端 ForkProfile 注册表与身份识别（全部 fork 知识只存在于此包数据） |
| `internal/hub` | SQLite 中枢（版本链/tombstone/origin_fork/对账查询） |
| `internal/{collect,deploy}` | 收集/投放编排 |
| `internal/{filestore,guard}` | 内容寻址仓与链接分级、进程守卫与备份 |
| `internal/{diff,fixture,itests}` | 对账查询、测试库构造器、集成验收断言 |
| `internal/appsvc` + `src/` + `main.go` | mygo Bind 服务层与 vanilla TS 四页前端 |
| `docs/design.md` | 施工蓝图：**§0 产品行为模型（B1-B12、三方判定、一轮流程、G 差距清单）优先级最高**，其后 18 硬约束 + HB1-HB4；改同步行为/内核前必读 |
| `docs/research/` | 四端差异的唯一事实源（自包含，reference/ 已删除） |
| `GLOSSARY.md` / `docs/adr/` | 领域术语表与架构决策记录 |
| `justfile` / `manifest.scm` / `lzsync.scm` | 开发指令封装 / guix 开发环境（direnv use guix 加载）/ guix 打包定义（`guix build -f lzsync.scm`） |
| `scripts/fetch-librealm.sh` | native 库可复现获取（钉死 realm-core 20.1.1） |

## 常用命令（justfile，门禁）

```bash
just go-check    # go build ./... && go vet ./...
just test        # go test ./... -count=1（含 itests 集成断言）
just typecheck   # tsc --noEmit
just generate    # mygo generate（改 appsvc Bind 面后重生 src/mygo.ts）
just build       # mygo build 完整编译打包 → build/linux-amd64/
just package     # guix 打包（先 build；lzsync.scm 装配进 store，wrap 好运行环境）
just gates       # 四道门禁一次跑全
just run         # 启动主程序（调试入口）：优先 guix 包 binary，回退直跑 build 产物
just dev         # mygo dev 热重载（vite 5173）
```

## 架构边界与硬约束

- **C-1 自包含**：fork 知识（列白名单、目录名、版本编码）只进 `docs/research/` 摘录与 `internal/forks` 包数据，**代码与文档零引用 reference/**（该镜像已删除）。
- **依赖方向**：`appsvc → {collect,deploy} → {hub,diff,filestore,guard} → {realmc,forks}`；`realmc` 之上无业务、`hub` 之下无 realm。
- **内核不知道任何具体 fork**：新增第五个 fork = 新增一份 ForkProfile，不改内核。
- **同步行为以 `docs/design.md` §0 为准**：收集只增不改、各端差异分别存；导出取全部目标端的全局交集；只传播基线三方判定能确定的改动，判不准保留差异；一切同步入口遵循设置（板块开关 + 端的来源/目标角色）。施工消除 §0.5 差距项后在该表标注。
- **数据安全 18 条硬约束**见 `docs/design.md` §1（写前备份、不改 schema 版本、软删优先、占用拒写、DriftGuard 等），施工与修改不得违背。
- 决策级改动先读 `docs/adr/`（为何 SQLite 中枢而非库对库直连、为何 purego+realm-core、为何绝不迁移 schema 版本、为何基线三方判定 + 全局交集）。

## 协作约定

- 修改前先读本文件、`docs/design.md` 相关章节与 `GLOSSARY.md` 术语。
- 保持改动最小化，沿用现有风格；完成一小部分即 commit 一次（规范见 `~/.config/git/gitmessage`，带 Assisted-by trailer，不自动 push）。
- 必须使用 direnv 套件来进行环境管理，若 manifest.scm 存在时则优先利用它来管理环境（改 manifest.scm 后 `direnv reload` 生效；webview 运行库与工具链全部声明其中）。

## 已知坑

- **bun 已随 guix 进系统 PATH**（home profile 现为 1.4.4，guix 现行包 1.4.3，manifest 环境以 guix 现行为准）：`mygo.config.ts` 子进程命令与 justfile 均直接用 `bun`；~/.npm-global 时代的 npm 全局包已删除，勿再引用绝对路径或 npm 回退。
- **webview 运行库（webkitgtk-for-gtk3 等）只在 manifest 环境里可见**：guix 无全局库路径，`just run`/`just dev` 经 `GUIX_ENVIRONMENT/lib` 补 LD_LIBRARY_PATH；裸跑产物二进制会报缺 libglib/libwebkit。
- **realm-core 构建必须 gcc-toolchain@13**：GCC 16 与 glibc 的 isnan 宏冲突报 196 错，`-include cmath` 等救不了；其余三个坑（REALM_NO_TESTS、librealm-ffi target、NuGet wrappers 无 C API）记录在 `scripts/fetch-librealm.sh` 头注。
- **Mimosa 安全门禁**：`internal/fixture` 的 MD5（osu! 谱面哈希格式要求）与测试辅助函数（copyFile/exec.Command 硬编码路径）是已裁定误报，已登记 `mimosa-ignore` 行注释（只对单文件扫描/IDE 层生效；1.0.4 的 L3 深扫不读注释）。L3 commit 门禁经用户拍板降为 warn 档（`.mimosa/config.json`，已 gitignore）：扫描与风险提示照常输出但不拦 commit；写入门禁仍全拦——编辑触到误报行本身被拦时，临时写 `{"hook":{"blockMode":"warn"}}` 过桥、用完 trash-put 恢复。勿整体关闭门禁。
- 删除/补丁类命令在本环境冻结：文件清理走 trash-put，改动走 Edit 工具。

## 技能索引

- `.agents/skills/` — 项目技能与工作流（mygo-maintenance：改 UI/appsvc 前先读）
