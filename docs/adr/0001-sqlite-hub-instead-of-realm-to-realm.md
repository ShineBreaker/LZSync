# SQLite 中枢 + 链接投放，而非库对库直连

先例工具 EZRealmSync（Ez2Lazer 官方）在两份 realm 库之间直接复制对象；LZSync 改为 hub-and-spoke：SQLite 中枢做单一真相源，实体文件入自有内容寻址仓，游戏侧以"链接 + 自建 realm 行"投放。动因是用户判断 realm 生态已停更（realm-dotnet 停摆、Atlas Device Sync 关停）——把 LZSync 的核心数据放在 SQLite，上游任何一天弃用 realm，只需替换边缘的收集/投放适配层。代价是一次同步语义上拆成收集与投放两段、多一层仓与链接管理。

## Considered Options

- 库对库直连（EZRealmSync 模式）：更简单，但每次同步都要实时读两端 realm，且核心数据形态被 realm 绑架。
- 完全不碰 realm（推送导入 .osz/.osk 容器）：读不了库内现状、做不了对账，能力天花板过低，仅保留为无 native 库环境的降级路径（设计路线④）。

## Consequences

- 对账与预览是 hub 内 SQL 查询，无需打开任何 realm 库。
- 收仓把游戏侧实体文件"归公"到仓内，游戏侧只剩链接；游戏端 Cleanup 删除零引用文件时删的是链接，不伤仓内源。
- fork 侧 realm 库永远可以被对应客户端直接使用——LZSync 写坏任何一端都不影响其他端（配合 ADR-0003 的安全约束）。
