// 双语词典与语言状态（zh-CN / en）：前端展示文案与后端稳定字符串翻译。
// 文案规则见 docs/DESIGN.md：名词用实体名，动词最小集，不解释概念。

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
    // ===== 导航与壳 =====
    "nav.overview": "概览",
    "nav.beatmaps": "谱面集",
    "nav.collections": "收藏",
    "nav.skins": "皮肤",
    "nav.scores": "成绩",
    "nav.sync": "同步",
    "nav.clients": "客户端",
    "nav.settings": "设置",
    "nav.sectionData": "数据",
    "sidebar.clients": "客户端",
    "sidebar.running": "{0} 运行中",
    "sidebar.clientsSummary": "{0} 端 · {1}",
    "sidebar.noClients": "未添加",
    "sidebar.statusUnavailable": "状态未知",
    "global.gameRunning": "{0} 运行中",
    "search.placeholder": "搜索",

    // ===== 数据行状态（悬浮层端态） =====
    "st.synced": "一致",
    "st.update": "有更新",
    "st.conflict": "冲突",
    "st.needsConfirm": "需确认",
    "st.targetOnly": "仅目标有",
    "st.missing": "—",
    "st.ok": "✓",
    "st.old": "⚠ 旧",

    // ===== 通用 =====
    "kind.beatmap_set": "谱面集",
    "kind.beatmap": "难度",
    "kind.collection": "收藏",
    "kind.skin": "皮肤",
    "kind.score": "成绩",
    "kind.key_binding": "键位",
    "kind.ruleset_setting": "规则集",
    "kind.mod_preset": "Mod",
    "action.create": "新建",
    "action.update": "更新",
    "action.version-replace": "替换版本",

    // 兼容 ui.ts 既有 helper（计划执行报告用）
    "cat.create": "新增",
    "cat.target-only": "仅目标有",
    "cat.conflict": "冲突",
    "cat.version-update": "有更新",
    "cat.restore": "已删除",
    "vsrc.realm-open": "实时",
    "vsrc.filename": "文件名",
    "vsrc.snapshot-cache": "缓存",
    "vsrc.unknown-running": "未知",

    // ===== 数据浏览页 =====
    "browse.title": "谱面集",
    "browse.total": "{0} 项",
    "browse.selected": "已选 {0}",
    "browse.syncBtn": "同步全部",
    "browse.pushAll": "已下发 {0} 项到全部 {1} 端",
    "browse.pushAllFail": "已下发到 {0}/{1} 端",
    "browse.exportBtn": "导出",
    "browse.filterAll": "全部",
    "browse.filterDiff": "仅看差异",
    "browse.filterConflict": "冲突",
    "browse.filterNeedsConfirm": "需确认",
    "browse.empty": "暂无数据",
    "browse.loading": "加载中…",
    "browse.collectedBy": "最近拉取自 {0}",
    "browse.hubVersion": "v{0}",
    "browse.diff": "{0} 难度",
    "browse.files": "{0} MiB",
    "browse.beatmaps": "{0} 谱面",
    "browse.confirmTitle": "确认要替换目标端的旧版本吗？",
    "browse.conflictFields": "差异字段",
    "browse.conflictHub": "中枢",
    "browse.conflictFork": "目标端",
    "browse.deletedOn": "删除于 {0}",
    "browse.showDiff": "查看差异",

    // ===== 同步页 =====
    "sync.title": "同步",
    "sync.step1": "拉取",
    "sync.step2": "同步到",
    "sync.step3": "还原",
    "sync.pullBtn": "拉取",
    "sync.pullAll": "全部拉取",
    "sync.pullDesc": "把客户端最新数据读到中枢",
    "sync.pushBtn": "同步",
    "sync.pushDesc": "把选中的数据写到目标端",
    "sync.checkGuard": "目标未运行",
    "sync.checkBackup": "自动备份",
    "sync.checkPlan": "{0} 个动作",
    "sync.running": "同步中…",
    "sync.done": "完成",
    "sync.failed": "中断",
    "sync.cancel": "取消",
    "sync.applied": "新增 {0} · 更新 {1} · 跳过 {2}",
    "sync.noPlan": "先到数据页勾选条目",
    "sync.backup": "备份 → {0}",
    "sync.restoreFrom": "从备份还原",
    "sync.restoreTarget": "目标",
    "sync.restorePath": "路径",
    "sync.restoreBtn": "还原",
    "sync.restoreDone": "已还原",

    // ===== 客户端页 =====
    "clients.title": "客户端",
    "clients.scan": "自动扫描",
    "clients.add": "手动注册",
    "clients.pathPlaceholder": "路径…",
    "clients.remove": "移除",
    "clients.running": "运行中",
    "clients.unidentified": "未识别",
    "clients.assignFork": "选择类型",
    "clients.confirmAssign": "确认",
    "clients.empty": "未添加客户端",
    "clients.added": "已添加 {0}",
    "clients.version": "v{0}",

    // ===== 设置页 =====
    "set.language": "语言",
    "set.backend": "后端",
    "set.mode": "模式",
    "set.modeReal": "正常",
    "set.modeMock": "演示",
    "set.modeFallback": "降级",
    "set.nativeLib": "引擎",
    "set.nativeOk": "就绪",
    "set.nativeFail": "未加载",
    "set.paths": "存储",
    "set.dataDir": "数据目录",
    "set.hubDb": "中枢数据库",
    "set.backupsDir": "备份目录",
    "set.backupsCount": "{0} 份",
    "set.guardSection": "进程",
    "set.linkSection": "链接",
    "set.linkOk": "支持",
    "set.linkFail": "不支持",
  },
  en: {
    // ===== Navigation & shell =====
    "nav.overview": "Overview",
    "nav.beatmaps": "Beatmaps",
    "nav.collections": "Collections",
    "nav.skins": "Skins",
    "nav.scores": "Scores",
    "nav.sync": "Sync",
    "nav.clients": "Clients",
    "nav.settings": "Settings",
    "nav.sectionData": "Data",
    "sidebar.clients": "Clients",
    "sidebar.running": "{0} running",
    "sidebar.clientsSummary": "{0} · {1}",
    "sidebar.noClients": "None",
    "sidebar.statusUnavailable": "Unknown",
    "global.gameRunning": "{0} running",
    "search.placeholder": "Search",

    // ===== Sync states =====
    "st.synced": "Synced",
    "st.update": "Update",
    "st.conflict": "Conflict",
    "st.needsConfirm": "Confirm",
    "st.targetOnly": "Target only",
    "st.missing": "—",
    "st.ok": "✓",
    "st.old": "⚠",

    // ===== Common =====
    "kind.beatmap_set": "Beatmap Set",
    "kind.beatmap": "Beatmap",
    "kind.collection": "Collection",
    "kind.skin": "Skin",
    "kind.score": "Score",
    "kind.key_binding": "Key",
    "kind.ruleset_setting": "Ruleset",
    "kind.mod_preset": "Mod",
    "action.create": "New",
    "action.update": "Update",
    "action.version-replace": "Replace",

    "cat.create": "New",
    "cat.target-only": "Target only",
    "cat.conflict": "Conflict",
    "cat.version-update": "Update",
    "cat.restore": "Deleted",
    "vsrc.realm-open": "Live",
    "vsrc.filename": "Filename",
    "vsrc.snapshot-cache": "Cached",
    "vsrc.unknown-running": "Unknown",

    // ===== Browse page =====
    "browse.title": "Beatmap Sets",
    "browse.total": "{0}",
    "browse.selected": "{0} selected",
    "browse.syncBtn": "Sync All",
    "browse.pushAll": "Pushed {0} item(s) to all {1} forks",
    "browse.pushAllFail": "Pushed to {0}/{1} forks",
    "browse.exportBtn": "Export",
    "browse.filterAll": "All",
    "browse.filterDiff": "Changed",
    "browse.filterConflict": "Conflicts",
    "browse.filterNeedsConfirm": "Needs confirm",
    "browse.empty": "Nothing here",
    "browse.loading": "Loading…",
    "browse.collectedBy": "From {0}",
    "browse.hubVersion": "v{0}",
    "browse.diff": "{0} diffs",
    "browse.files": "{0} MiB",
    "browse.beatmaps": "{0} beatmaps",
    "browse.confirmTitle": "Replace older version on target?",
    "browse.conflictHub": "Hub",
    "browse.conflictFork": "Target",
    "browse.deletedOn": "Deleted {0}",
    "browse.conflictFields": "Differences",
    "browse.showDiff": "Diff",

    // ===== Sync page =====
    "sync.title": "Sync",
    "sync.step1": "Pull",
    "sync.step2": "Sync to",
    "sync.step3": "Restore",
    "sync.pullBtn": "Pull",
    "sync.pullAll": "Pull all",
    "sync.pullDesc": "Read client data into hub",
    "sync.pushBtn": "Sync",
    "sync.pushDesc": "Write selected data to target",
    "sync.checkGuard": "Target not running",
    "sync.checkBackup": "Auto backup",
    "sync.checkPlan": "{0} actions",
    "sync.running": "Syncing…",
    "sync.done": "Done",
    "sync.failed": "Stopped",
    "sync.cancel": "Cancel",
    "sync.applied": "+{0} / ~{1} / skip {2}",
    "sync.noPlan": "Select items in Browse first",
    "sync.backup": "Backup → {0}",
    "sync.restoreFrom": "Restore from backup",
    "sync.restoreTarget": "Target",
    "sync.restorePath": "Path",
    "sync.restoreBtn": "Restore",
    "sync.restoreDone": "Restored",

    // ===== Clients page =====
    "clients.title": "Clients",
    "clients.scan": "Scan",
    "clients.add": "Add",
    "clients.pathPlaceholder": "Path…",
    "clients.remove": "Remove",
    "clients.running": "Running",
    "clients.unidentified": "Unidentified",
    "clients.assignFork": "Type",
    "clients.confirmAssign": "Confirm",
    "clients.empty": "No clients",
    "clients.added": "Added {0}",
    "clients.version": "v{0}",

    // ===== Settings page =====
    "set.language": "Language",
    "set.backend": "Backend",
    "set.mode": "Mode",
    "set.modeReal": "Active",
    "set.modeMock": "Demo",
    "set.modeFallback": "Fallback",
    "set.nativeLib": "Engine",
    "set.nativeOk": "Ready",
    "set.nativeFail": "Unavailable",
    "set.paths": "Storage",
    "set.dataDir": "Data dir",
    "set.hubDb": "Hub DB",
    "set.backupsDir": "Backups",
    "set.backupsCount": "{0}",
    "set.guardSection": "Processes",
    "set.linkSection": "Links",
    "set.linkOk": "Yes",
    "set.linkFail": "No",
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
 * 后端字符串翻译：中文模式剥离内部章节号，英文模式做轻量映射。
 */
export function trBackend(s: string): string {
  if (!s) return s;
  if (currentLang === "zh") {
    return cleanZhBackend(s);
  }
  return translateToEn(s);
}

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
  "预演数据中不存在该键（快照可能已过期，请刷新预览）": "Item not in preview (refresh)",
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
  "入 hub": "Saving",
  "收仓+回链": "Archiving",
  "守卫复查": "Checking",
  "自动备份": "Backup",
  "建链接": "Linking",
  "事务写入": "Writing",
  "结构校验": "Verifying",
};

function translateToEn(s: string): string {
  const exact = EN_EXACT[s];
  if (exact) return exact;

  const verMatch = /^(\d+)\s*=\s*官方\s*(\d+)\s*\+\s*(\S+)\s*(\d+)$/.exec(s);
  if (verMatch) {
    return `${verMatch[1]} (upstream ${verMatch[2]} + ${verMatch[3]} ${verMatch[4]})`;
  }

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
    return `Pulling ${s.slice("开始收集 ".length)}…`;
  }
  if (s.startsWith("写前自动备份：")) {
    return `Backup: ${s.slice("写前自动备份：".length)}`;
  }

  return s
    .replace(/^appsvc:\s*/, "")
    .replace(/^guard:\s*/, "")
    .replace(/^deploy:\s*/, "")
    .replace(/^collect:\s*/, "")
    .replace(/目标游戏进程正在运行，请先关闭游戏后再同步/g, "Game is running")
    .replace(/目标库正被其他进程占用，请先关闭游戏后再同步/g, "Database in use")
    .replace(/已有同步任务在执行，请等待完成或取消/g, "Sync already running")
    .replace(/尚无投放计划，请先在对比预览页勾选并生成计划/g, "Select items first")
    .replace(/未勾选任何条目/g, "No items selected")
    .replace(/请选择目标 fork/g, "Select a target")
    .replace(/请选择对比双方 fork/g, "Select both forks")
    .replace(/对比双方不能是同一个 fork/g, "Source and target must differ")
    .replace(/运行中：/g, "running: ")
    .replace(/谱面集 /g, "Beatmap Set ")
    .replace(/皮肤 /g, "Skin ")
    .replace(/成绩 /g, "Score ");
}
