// 双语词典与语言状态（zh-CN / en）：前端展示文案与后端稳定字符串翻译。

export type Lang = "zh" | "en";

const STORAGE_KEY = "lzsync.lang";

function detectLang(): Lang {
  try {
    const saved = localStorage.getItem(STORAGE_KEY);
    if (saved === "zh" || saved === "en") return saved;
  } catch {
    // 无 localStorage 环境回退浏览器语言。
  }
  const nav = typeof navigator !== "undefined" ? navigator.language.toLowerCase() : "zh";
  return nav.startsWith("en") ? "en" : "zh";
}

let currentLang: Lang = detectLang();
const listeners = new Set<() => void>();

export function getLang(): Lang {
  return currentLang;
}

export function setLang(lang: Lang): void {
  if (currentLang === lang) return;
  currentLang = lang;
  try {
    localStorage.setItem(STORAGE_KEY, lang);
  } catch {
    // 忽略存储限制。
  }
  document.documentElement.lang = lang === "en" ? "en" : "zh-CN";
  for (const fn of listeners) {
    fn();
  }
}

export function onLangChange(fn: () => void): void {
  listeners.add(fn);
}

const DICT = {
  zh: {
    // 导航与壳
    "nav.libraries": "客户端库",
    "nav.compare": "差异对比",
    "nav.run": "收集与投放",
    "nav.settings": "设置",
    "sidebar.noForks": "未添加库",
    "sidebar.statusUnavailable": "状态未知",
    "global.gameRunning": "游戏运行中，请先关闭游戏再执行同步。",

    // 枚举与徽标
    "cat.create": "新增",
    "cat.target-only": "目标独有",
    "cat.conflict": "冲突",
    "cat.version-update": "版本更新",
    "cat.restore": "已删除",
    "kind.all": "全部",
    "kind.beatmap_set": "谱面集",
    "kind.beatmap": "难度",
    "kind.collection": "收藏夹",
    "kind.skin": "皮肤",
    "kind.score": "成绩",
    "kind.key_binding": "键位",
    "kind.ruleset_setting": "规则集设置",
    "kind.mod_preset": "Mod 预设",
    "action.create": "新建",
    "action.update": "更新",
    "action.version-replace": "替换版本",
    "vsrc.realm-open": "实时读取",
    "vsrc.filename": "文件名推断",
    "vsrc.snapshot-cache": "缓存快照",
    "vsrc.unknown-running": "运行中",

    // 库管理页
    "lib.scan": "自动扫描",
    "lib.pathPlaceholder": "输入 client.realm 文件路径",
    "lib.register": "添加",
    "lib.empty": "暂未添加客户端库。点击「自动扫描」或手动输入路径。",
    "lib.version": "版本 {0}",
    "lib.running": "运行中（{0}）",
    "lib.idle": "已关闭",
    "lib.remove": "移除",
    "lib.dataDir": "数据目录",
    "lib.realmFile": "数据库",
    "lib.filesDir": "文件目录",
    "lib.filesCount": "{0} 项",
    "lib.scanDone": "扫描完成，发现 {0} 个库",
    "lib.enterPath": "请输入 client.realm 路径",
    "lib.registered": "已添加 {0}（版本 {1}）",
    "lib.confirmAssign": "确认",
    "lib.assigned": "已添加 {0}",
    "lib.unidentified": "无法自动识别（版本 {0}），请选择客户端类型：",
    "lib.unknownVer": "未知",

    // 对比预览页
    "cmp.modePreview": "投放预览（中枢 → 目标库）",
    "cmp.modeForks": "跨端对比（源库 ↔ 目标库）",
    "cmp.query": "对比",
    "cmp.buildPlan": "生成投放计划",
    "cmp.filterAll": "全部",
    "cmp.filterCreate": "新增",
    "cmp.filterConflict": "冲突",
    "cmp.filterVersion": "版本更新",
    "cmp.filterRestore": "已删除",
    "cmp.filterTargetOnly": "目标独有",
    "cmp.querying": "正在对比…",
    "cmp.noSelection": "请先勾选要同步的条目",
    "cmp.planBuilt": "已生成 {0} 的投放计划：执行 {1} 项，跳过 {2} 项。请前往「收集与投放」页执行。",
    "cmp.exportBtn": "导出文件",
    "cmp.exportTitle": "导出为 .osz / .osk / .osr 并调用默认程序打开",
    "cmp.exported": "已导出 {0} 个文件：{1}",
    "cmp.emptyHint": "选择客户端后点击「对比」。",
    "cmp.beatmapHint": "难度随所属谱面集一起同步，无需单独勾选。",
    "cmp.noMatch": "无匹配项。",
    "cmp.needsConfirmTitle": "待确认（同名谱面，默认跳过）",
    "cmp.needsConfirmDesc": "元数据匹配到同名谱面，勾选后将替换目标端旧版本。",
    "cmp.otherDiffs": "常规差异",
    "cmp.weakBadge": "同名匹配{0}",
    "cmp.strongBadge": "ID 匹配{0}",
    "cmp.deletedAt": "删除于 {0}",
    "cmp.skipBadge": "跳过：{0}",
    "cmp.field": "字段",
    "cmp.hubValue": "中枢值",
    "cmp.forkValue": "目标值",
    "cmp.diffFields": "查看差异",
    "cmp.userUnchecked": "未勾选",
    "cmp.statsMain": "已选 {0} 项 / 跳过 {1} 项",
    "cmp.noDiffData": "暂无数据",

    // 同步执行页
    "run.collectTitle": "收集（客户端 → 中枢）",
    "run.collectDesc": "读取客户端数据并归档文件至中枢。请先关闭对应游戏。",
    "run.collectBtn": "收集",
    "run.cardStats": "库 {0} · 文件 {1}",
    "run.collectDone": "收集完成：{0}；归档 {1} 个文件（跳过 {2}）{3}",
    "run.noEntities": "无变更",
    "run.skippedPrefix": "；跳过：",
    "run.deployTitle": "投放（中枢 → 客户端）",
    "run.noPlan": "暂无投放计划，请先在「差异对比」页生成。",
    "run.checkGuard": "进程检查",
    "run.guardOk": "通过（游戏未运行）",
    "run.guardFail": "{0} 正在运行，请先关闭",
    "run.checkBackup": "自动备份",
    "run.backupOk": "写入前自动备份 {0}",
    "run.backupUnreg": "目标库未添加",
    "run.checkPlan": "计划摘要",
    "run.planSummary": "目标 {0} · 执行 {1} 项 · 跳过 {2} 项",
    "run.deployBtn": "开始投放",
    "run.deployConfirmBtn": "再次点击确认投放",
    "run.deployArmedHint": "写入前会自动备份目标库",
    "run.deployDone": "投放完成",
    "run.deployFailed": "投放中断：{0}",
    "run.reportTitle": "执行结果",
    "run.reportApplied": "已写入",
    "run.reportNone": "无",
    "run.gimmickWarn": "⚠ {0} 个谱面集含 DeltaLazer 专属数据，在非 Delta 客户端中编辑保存会丢失该部分内容。",
    "run.gimmickItem": "涉及谱面",
    "run.reportSkipped": "已跳过",
    "run.reportFailedBatch": "失败批次 #{0}（已回滚）",
    "run.reportNotRun": "未执行",
    "run.reportNotRunCount": "{0} 项",
    "run.reportRepointed": "状态变更",
    "run.reportBackup": "备份文件",
    "run.cancelBtn": "取消",
    "run.cancelling": "正在取消（当前批次完成后停止）…",
    "run.collecting": "正在收集…",
    "run.deploying": "正在投放…",
    "run.restoreTitle": "还原备份",
    "run.restorePlaceholder": "输入备份文件路径",
    "run.restoreBtn": "还原",
    "run.restoreNeedInput": "请选择客户端并输入备份路径",
    "run.restoreDone": "已从 {0} 还原至 {1}",

    // 设置页
    "set.langSection": "界面语言 / Language",
    "set.langLabel": "语言",
    "set.backendSection": "运行状态",
    "set.mode": "模式",
    "set.modeReal": "正常",
    "set.modeMock": "演示模式",
    "set.modeFallback": "降级模式",
    "set.reason": "原因",
    "set.nativeLib": "Realm 引擎",
    "set.nativeOk": "就绪",
    "set.nativeFail": "未加载：{0}",
    "set.pathsSection": "存储路径",
    "set.dataDir": "数据目录",
    "set.hubDb": "中枢数据库",
    "set.backupsDir": "备份目录",
    "set.backupsCount": "{0}（共 {1} 份）",
    "set.guardSection": "监控进程名",
    "set.linkSection": "文件链接支持",
    "set.symlink": "符号链接",
    "set.hardlink": "硬链接",
    "set.linkOk": "支持",
    "set.linkFail": "不支持",
  },
  en: {
    // Navigation & shell
    "nav.libraries": "Libraries",
    "nav.compare": "Compare",
    "nav.run": "Sync",
    "nav.settings": "Settings",
    "sidebar.noForks": "No libraries",
    "sidebar.statusUnavailable": "Status unknown",
    "global.gameRunning": "Game is running. Close it before syncing.",

    // Enums & badges
    "cat.create": "New",
    "cat.target-only": "Target only",
    "cat.conflict": "Conflict",
    "cat.version-update": "Update",
    "cat.restore": "Deleted",
    "kind.all": "All",
    "kind.beatmap_set": "Beatmap Sets",
    "kind.beatmap": "Beatmaps",
    "kind.collection": "Collections",
    "kind.skin": "Skins",
    "kind.score": "Scores",
    "kind.key_binding": "Key Bindings",
    "kind.ruleset_setting": "Rulesets",
    "kind.mod_preset": "Mod Presets",
    "action.create": "Created",
    "action.update": "Updated",
    "action.version-replace": "Replaced",
    "vsrc.realm-open": "Live",
    "vsrc.filename": "Filename",
    "vsrc.snapshot-cache": "Cached",
    "vsrc.unknown-running": "Running",

    // Libraries page
    "lib.scan": "Auto Scan",
    "lib.pathPlaceholder": "Path to client.realm",
    "lib.register": "Add",
    "lib.empty": "No libraries added. Click Auto Scan or add a client.realm path.",
    "lib.version": "v{0}",
    "lib.running": "Running ({0})",
    "lib.idle": "Idle",
    "lib.remove": "Remove",
    "lib.dataDir": "Data dir",
    "lib.realmFile": "Database",
    "lib.filesDir": "Files",
    "lib.filesCount": "{0} files",
    "lib.scanDone": "Found {0} libraries",
    "lib.enterPath": "Enter a client.realm path",
    "lib.registered": "Added {0} (v{1})",
    "lib.confirmAssign": "Confirm",
    "lib.assigned": "Added {0}",
    "lib.unidentified": "Unrecognized library (v{0}). Select fork:",
    "lib.unknownVer": "unknown",

    // Compare page
    "cmp.modePreview": "Deploy Preview (Hub → Target)",
    "cmp.modeForks": "Fork Diff (Source ↔ Target)",
    "cmp.query": "Compare",
    "cmp.buildPlan": "Create Plan",
    "cmp.filterAll": "All",
    "cmp.filterCreate": "New",
    "cmp.filterConflict": "Conflicts",
    "cmp.filterVersion": "Updates",
    "cmp.filterRestore": "Deleted",
    "cmp.filterTargetOnly": "Target only",
    "cmp.querying": "Comparing…",
    "cmp.noSelection": "Select at least one item",
    "cmp.planBuilt": "Plan ready for {0}: {1} to apply, {2} skipped. Go to Sync to run.",
    "cmp.exportBtn": "Export",
    "cmp.exportTitle": "Export as .osz / .osk / .osr and open",
    "cmp.exported": "Exported {0} file(s): {1}",
    "cmp.emptyHint": "Select a target and click Compare.",
    "cmp.beatmapHint": "Beatmap difficulties sync with their parent Beatmap Set.",
    "cmp.noMatch": "No matching items.",
    "cmp.needsConfirmTitle": "Needs Confirmation (Title Match, Skipped by Default)",
    "cmp.needsConfirmDesc": "Matched by metadata. Check to replace the older version on target.",
    "cmp.otherDiffs": "Changes",
    "cmp.weakBadge": "Title match{0}",
    "cmp.strongBadge": "ID match{0}",
    "cmp.deletedAt": "Deleted {0}",
    "cmp.skipBadge": "Skip: {0}",
    "cmp.field": "Field",
    "cmp.hubValue": "Hub",
    "cmp.forkValue": "Target",
    "cmp.diffFields": "Diff",
    "cmp.userUnchecked": "Unchecked",
    "cmp.statsMain": "{0} selected / {1} skipped",
    "cmp.noDiffData": "No items",

    // Sync (Run) page
    "run.collectTitle": "Collect (Fork → Hub)",
    "run.collectDesc": "Read fork data and archive files into Hub. Close the game first.",
    "run.collectBtn": "Collect",
    "run.cardStats": "DB {0} · Files {1}",
    "run.collectDone": "Collected: {0}; {1} files archived ({2} skipped){3}",
    "run.noEntities": "no changes",
    "run.skippedPrefix": "; skipped: ",
    "run.deployTitle": "Deploy (Hub → Fork)",
    "run.noPlan": "No active plan. Create one in Compare first.",
    "run.checkGuard": "Process check",
    "run.guardOk": "Ready (game closed)",
    "run.guardFail": "{0} is running — close it first",
    "run.checkBackup": "Backup",
    "run.backupOk": "Auto-backup {0}",
    "run.backupUnreg": "Target not registered",
    "run.checkPlan": "Plan",
    "run.planSummary": "Target {0} · {1} to apply · {2} skipped",
    "run.deployBtn": "Deploy",
    "run.deployConfirmBtn": "Click Again to Confirm",
    "run.deployArmedHint": "Target database will be backed up before writing",
    "run.deployDone": "Deploy finished",
    "run.deployFailed": "Deploy stopped: {0}",
    "run.reportTitle": "Deploy Report",
    "run.reportApplied": "Applied",
    "run.reportNone": "None",
    "run.gimmickWarn": "⚠ {0} beatmap set(s) contain DeltaLazer gimmick data. Saving them in another fork's editor will strip those gimmicks.",
    "run.gimmickItem": "Beatmap",
    "run.reportSkipped": "Skipped",
    "run.reportFailedBatch": "Failed batch #{0} (rolled back)",
    "run.reportNotRun": "Remaining",
    "run.reportNotRunCount": "{0} items",
    "run.reportRepointed": "Adjusted",
    "run.reportBackup": "Backup",
    "run.cancelBtn": "Cancel",
    "run.cancelling": "Cancelling after current batch…",
    "run.collecting": "Collecting…",
    "run.deploying": "Deploying…",
    "run.restoreTitle": "Restore Backup",
    "run.restorePlaceholder": "Backup file path",
    "run.restoreBtn": "Restore",
    "run.restoreNeedInput": "Select a fork and enter a backup path",
    "run.restoreDone": "Restored {1} from {0}",

    // Settings page
    "set.langSection": "Language / 界面语言",
    "set.langLabel": "Language",
    "set.backendSection": "Backend",
    "set.mode": "Mode",
    "set.modeReal": "Active",
    "set.modeMock": "Demo (Mock)",
    "set.modeFallback": "Fallback (Mock)",
    "set.reason": "Note",
    "set.nativeLib": "Realm Engine",
    "set.nativeOk": "Loaded",
    "set.nativeFail": "Unavailable: {0}",
    "set.pathsSection": "Storage Paths",
    "set.dataDir": "Data dir",
    "set.hubDb": "Hub DB",
    "set.backupsDir": "Backups",
    "set.backupsCount": "{0} ({1} backups)",
    "set.guardSection": "Watched Processes",
    "set.linkSection": "File Link Support",
    "set.symlink": "Symlink",
    "set.hardlink": "Hardlink",
    "set.linkOk": "Supported",
    "set.linkFail": "Unsupported",
  },
} as const;

export type DictKey = keyof typeof DICT.zh;

export function t(key: DictKey, ...args: (string | number)[]): string {
  const table = DICT[currentLang];
  let out: string = table[key] ?? DICT.zh[key];
  for (let i = 0; i < args.length; i++) {
    out = out.replace(`{${i}}`, String(args[i]));
  }
  return out;
}

/**
 * 将后端返回的中文说明、摘要、跳过原因或错误信息做轻量化清理与英文化映射，
 * 不改动 Go 后端内部测试契约。
 */
export function trBackend(s: string): string {
  if (!s) return s;
  if (currentLang === "zh") {
    return cleanZhBackend(s);
  }
  return translateToEn(s);
}

/** 中文模式下剥离后端字符串里的内部章节号与冗余术语注释。 */
function cleanZhBackend(s: string): string {
  return s
    .replace(/^appsvc:\s*/, "")
    .replace(/^guard:\s*/, "")
    .replace(/^deploy:\s*/, "")
    .replace(/^collect:\s*/, "")
    .replace(/（§[^）]*）/g, "")
    .replace(/（[A-Z]\d+[^）]*）/g, "")
    .replace(/，§[\d.-]+/g, "")
    .replace(/元数据弱键「疑似」版本更新，需用户拍板/g, "同名谱面待确认");
}

const EN_EXACT: Record<string, string> = {
  "未知（运行中）": "Unknown (running)",
  "元数据弱键「疑似」版本更新，需用户拍板（§5.4 三选一）": "Title match, needs confirmation",
  "元数据弱键「疑似」版本更新，需用户拍板": "Title match, needs confirmation",
  "Protected 系统谱面集": "Protected system beatmap set",
  "Ez 外部托管谱面集": "Ez2Lazer external beatmap set",
  "软删谱面集（DeletePending）": "Pending deletion beatmap set",
  "Hidden 难度": "Hidden beatmap difficulty",
  "fork 专有规则集难度（OwnRulesets）": "Fork-exclusive ruleset difficulty",
  "软删皮肤（DeletePending）": "Pending deletion skin",
  "Protected 内置皮肤": "Built-in protected skin",
  "fork 专有皮肤（OwnSkins 指纹）": "Fork-exclusive skin",
  "皮肤 Hash 为空（无法判定身份）": "Skin has empty hash",
  "软删成绩（DeletePending）": "Pending deletion score",
  "成绩 BeatmapHash 为空（无谱面锚点）": "Score missing BeatmapHash",
  "fork 专有规则集成绩（OwnRulesets）": "Fork-exclusive ruleset score",
  "规则集市场行成绩（OnlineID 超内置域，服务器市场产物不跨端）": "Custom server ruleset score",
  "fork 专有判定档成绩（判定语义列非 0，官方端重算会错）": "Fork-specific judgement score",
  "软删模组预设（DeletePending）": "Pending deletion mod preset",
  "源端无 game.ini（从未启动过客户端，ini 键未收集）": "No game.ini on source",
  "目标已出现同键行：新建 → 就地更新": "Target row exists: Create → Update",
  "目标缺同键行：就地更新 → 新建": "Target row missing: Update → Create",
  "目标已是 canonical 值（重续判定：已存在 → 跳过）": "Already up to date",
  "目标已软删（计划后删除），跳过防复活（§5.4 删除不复活）": "Deleted on target, skipped",
  "预演数据中不存在该键（快照可能已过期，请刷新预览）": "Item not in preview (refresh Compare)",
  "成绩 Statistics 含目标端不认识的 HitResult 键（过滤后写入，D7）": "Filtered unsupported HitResult keys",
  "守卫复查通过": "Process check passed",
  "Mock 后端仅演示：未生成真实容器、未拉起客户端": "Demo mode: no container files written",
  "LZSYNC_MOCK=1 强制 Mock 后端（M-4 开发模式）": "LZSYNC_MOCK=1 demo mode",
  "Mock 后端不加载 native 库（M-4 冒烟模式）": "Not loaded in demo mode",
  "按 §5.3 自动分级：符号链接 → 硬链接（同卷）→ 复制兜底；探测不可用时逐级降级": "Symlink → Hardlink (same drive) → Copy",
  "Mock 探测结果（真实后端按 §5.3 现场探测）": "Demo probe result",
};

const EN_STAGE: Record<string, string> = {
  "只读投影": "Reading",
  "入 hub": "Saving to Hub",
  "收仓+回链": "Archiving files",
  "守卫复查": "Checking processes",
  "自动备份": "Backing up",
  "建链接": "Linking files",
  "事务写入": "Writing database",
  "结构校验": "Verifying schema",
};

function translateToEn(s: string): string {
  const exact = EN_EXACT[s];
  if (exact) return exact;

  // 磁盘版本标签: "52011 = 官方 52 + ez2lazer 11"
  const verMatch = /^(\d+)\s*=\s*官方\s*(\d+)\s*\+\s*(\S+)\s*(\d+)$/.exec(s);
  if (verMatch) {
    return `${verMatch[1]} (upstream ${verMatch[2]} + ${verMatch[3]} ${verMatch[4]})`;
  }

  // 摘要格式
  const colMatch = /^收藏夹「(.+)」$/.exec(s);
  if (colMatch) return `Collection "${colMatch[1]}"`;

  const skinMatch = /^皮肤「(.+?)」（(.+)）$/.exec(s);
  if (skinMatch) return `Skin "${skinMatch[1]}" (${skinMatch[2]})`;

  const skinSimple = /^皮肤「(.+)」$/.exec(s);
  if (skinSimple) return `Skin "${skinSimple[1]}"`;

  const scoreMatch = /^成绩\s+(\S+)\s+·\s+(\d+)\s+分\s+·\s+(.+)$/.exec(s);
  if (scoreMatch) return `Score ${scoreMatch[1]} · ${scoreMatch[2]} pts · ${scoreMatch[3]}`;

  const kbMatch = /^键位\s+(.+)$/.exec(s);
  if (kbMatch) return `Key Binding ${kbMatch[1]!.replace(/^全局/, "Global")}`;

  const rsMatch = /^规则集设置\s+(.+)$/.exec(s);
  if (rsMatch) return `Ruleset Setting ${rsMatch[1]}`;

  const mpMatch = /^模组预设\s+(.+)$/.exec(s);
  if (mpMatch) return `Mod Preset ${mpMatch[1]}`;

  // 进度消息
  const batchMatch = /^批\s*(\d+)\/(\d+)\s*·\s*([^：:]+)[：:]\s*(\d+)\/(\d+)$/.exec(s);
  if (batchMatch) {
    const st = EN_STAGE[batchMatch[3]!] ?? batchMatch[3];
    return `Batch ${batchMatch[1]}/${batchMatch[2]} · ${st}: ${batchMatch[4]}/${batchMatch[5]}`;
  }
  const stepMatch = /^([^：:]+)[：:]\s*(\d+)\/(\d+)$/.exec(s);
  if (stepMatch && EN_STAGE[stepMatch[1]!]) {
    return `${EN_STAGE[stepMatch[1]!]}: ${stepMatch[2]}/${stepMatch[3]}`;
  }
  if (s.startsWith("开始收集 ")) {
    return `Collecting ${s.slice("开始收集 ".length)}…`;
  }
  if (s.startsWith("写前自动备份：")) {
    return `Backup created: ${s.slice("写前自动备份：".length)}`;
  }

  // 常见错误与跳过原因前缀转换
  return s
    .replace(/^appsvc:\s*/, "")
    .replace(/^guard:\s*/, "")
    .replace(/^deploy:\s*/, "")
    .replace(/^collect:\s*/, "")
    .replace(/目标游戏进程正在运行，请先关闭游戏后再同步/g, "Game is running. Close it before syncing")
    .replace(/目标库正被其他进程占用，请先关闭游戏后再同步/g, "Target database is in use. Close the game first")
    .replace(/已有同步任务在执行，请等待完成或取消/g, "A sync task is already running")
    .replace(/尚无投放计划，请先在对比预览页勾选并生成计划/g, "No active plan. Create one in Compare first")
    .replace(/未勾选任何条目/g, "No items selected")
    .replace(/请选择目标 fork/g, "Select a target fork")
    .replace(/请选择对比双方 fork/g, "Select both source and target forks")
    .replace(/对比双方不能是同一个 fork/g, "Source and target must be different")
    .replace(/运行中：/g, "running: ")
    .replace(/谱面集 /g, "Beatmap Set ")
    .replace(/皮肤 /g, "Skin ")
    .replace(/成绩 /g, "Score ");
}
