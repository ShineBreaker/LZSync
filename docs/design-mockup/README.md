# LZSync UI Mockup · 2026-10-25

> 用途：搬进 Figma 当底图迭代。源文件是 SVG（矢量，搬进去可拆图层），PNG 仅预览。

## 文件

| 文件 | 内容 | 搬 Figma 建议 |
|---|---|---|
| `tokens.svg/.png` | 品牌色 / 四 fork 身份色 / 语义色 / 差异分类徽标色 / 深色中性阶 / 字号间距 | 建 Figma Variables，命名沿用标签 |
| `icons.svg/.png` | 应用图标三候选（A 枢纽辐条 / B 库+环 / C 三角循环）+ 16-128px 缩放预览 | 拆三个 frame 单选 |
| `pages.svg/.png` | 四页合一画板：库管理 / 对比预览 / 同步执行 / 设置 | 整张贴进后按 ①②③④ 拆四个 frame |

## 关键设计决策

- **深色优先**：主基调 `#0E1116`，四页共用侧栏 + 全局警示条（游戏运行时锁定提示）
- **四 fork 身份色**：lazer=#66CCFF / delta=#C88BF5 / ez2lazer=#FF7EB6 / g0v0=#7CE8B8，用于头像色块、状态点、卡片左边条——同一颜色贯穿侧栏灯/卡片/执行页选择器，建立「这个 fork」的视觉记忆
- **图标推荐 A**：hub-and-spoke 直译（白辐条连中心圆到四色节点），16px 下节点仍可辨；B/C 备选
- **库管理页卡片**新增底部操作条（收集 / 投放预演快捷入口）——原本要跳页的操作在卡片上一键发起
- **执行页**顶部加四步指示条（收集 → 计划 → 投放 → 还原），让流程外显
- **对比页**底栏加全选/反选 + 跳过原因分类计数，差异行徽标沿分类色
- **待指认卡**用虚线边框 + 灰问号头像，跟已识别卡视觉分层

## 已知瑕疵（搬 Figma 时顺手修）

- 库管理 ez2lazer 卡底部「🔒 osu!.exe 运行中」行溢出卡片底边 8px → 卡片高度 +16 或行内移到操作条上方
- 对比页冲突行展开字段表紧贴行底 → 行内 padding-bottom 加到 12
- 全局警示条在四页重复贴图 → 搬 Figma 后做成 component 复用
- 侧栏「已管理库」分区与导航间距可再压 8px

## 未画（留给你决定要不要）

- 浅色版整页（tokens 已给深色阶，浅色 = 反转语义映射）
- 空态 / 加载态 / 错误态（现在各页只画了主路径）
- 对话框（移除库确认、还原确认）
- 响应式断点（mygo 桌面 app，假定 1280+）

## 实现侧映射

- 图标选定后 → `resources/icon.png`（1024×1024 PNG 导出），`main.go` embed 与 `lzsync.scm` 打包路径不变
- fork 色 → `src/style.css` 加 `--fork-lazer` 等变量，`src/pages/libraries.ts` 卡片左边条 + 头像按 `card.ForkID` 映射
- 步骤条 / 底栏全选 → `src/pages/run.ts` / `compare.ts` 结构微调
- i18n：新增 `lib.collect` / `lib.previewDeploy` / `cmp.selectAll` / `cmp.invertSel` / `run.step*` 等键
