// 双语文案（DESIGN.md §7）：页面只放允许词表内的短词，无解释句。
// t(key, ...args) 支持 {0}{1} 占位；trBackend 直译后端返回词。

export type Lang = "zh" | "en";

let lang: Lang = "zh";

export function setLang(l: Lang): void {
  lang = l;
}

export function getLang(): Lang {
  return lang;
}

const zh: Record<string, string> = {
  "nav.sync": "同步",
  "nav.beatmap_set": "谱面集",
  "nav.collection": "收藏",
  "nav.skin": "皮肤",
  "nav.score": "成绩",
  "nav.settings": "设置",

  "btn.sync": "同步",
  "btn.pulling": "拉取中…",
  "btn.writeN": "写入 {0} 项",
  "btn.writing": "写入中…",
  "btn.uptodate": "已是最新",
  "btn.cancel": "取消",
  "btn.close": "关闭",

  "lane.pull": "拉取",
  "lane.write": "写入",

  "client.running": "运行中",
  "client.scan": "扫描",
  "client.pickDir": "选择文件夹…",
  "client.restore": "还原…",
  "client.remove": "移除",
  "client.restoreAt": "还原 {0}",

  "decide.keep": "保留差异",
  "decide.update": "更新为新版本",
  "decide.restore": "恢复",

  "bar.staged": "已选 {0}",

  "result.created": "新增 {0}",
  "result.updated": "更新 {0}",
  "result.skipped": "跳过 {0}",
  "result.backup": "备份 {0}",
  "pend.unsupported": "不支持 {0}",

  "cov.has": "✓",
  "cov.incoming": "将写入",
  "cov.conflict": "冲突",
  "cov.confirm": "待确认",
  "cov.deleted": "已删除",
  "cov.unsupported": "不支持",
  "cov.noWrite": "不写入",
  "cov.none": "—",

  "inbox.baseline": "基准",

  "lib.search": "搜索",
  "lib.all": "全部",
  "lib.pending": "待处理",
  "lib.deleted": "已删除",
  "lib.deletedAt": "删除于 {0}",

  "set.title": "设置",
  "set.lang": "语言",
  "set.baseline": "基准端",
  "set.baselineSaved": "基准端已保存",
  "set.storage": "存储",
  "set.dataDir": "数据目录",
  "set.hubDb": "库数据库",
  "set.backups": "备份目录",
  "set.backupsCount": "{0} 个",
  "set.engine": "引擎",
  "set.engine.real": "正常",
  "set.engine.mock": "演示",
  "set.engine.fallback": "降级",
  "set.links": "链接支持",
  "set.sym": "符号链接",
  "set.hard": "硬链接",
  "set.guard": "游戏进程守卫",
  "set.guardHint": "运行中不写",

  "kind.beatmap_set": "谱面集",
  "kind.collection": "收藏",
  "kind.skin": "皮肤",
  "kind.score": "成绩",
  "kind.beatmap": "谱面",
  "kind.key_binding": "键位",
  "kind.ruleset_setting": "规则集设置",
  "kind.mod_preset": "Mod 预设",
  "kind.game_ini": "game.ini",

  "toast.saved": "已保存",
  "toast.removed": "已移除",
  "toast.restored": "已还原",
  "toast.assigned": "已识别",
  "toast.scanDone": "扫描完成",
  "toast.registered": "已注册 {0}",
};

const en: Record<string, string> = {
  "nav.sync": "Sync",
  "nav.beatmap_set": "Beatmap Sets",
  "nav.collection": "Collections",
  "nav.skin": "Skins",
  "nav.score": "Scores",
  "nav.settings": "Settings",

  "btn.sync": "Sync",
  "btn.pulling": "Pulling…",
  "btn.writeN": "Write {0} items",
  "btn.writing": "Writing…",
  "btn.uptodate": "Up to date",
  "btn.cancel": "Cancel",
  "btn.close": "Close",

  "lane.pull": "Pull",
  "lane.write": "Write",

  "client.running": "Running",
  "client.scan": "Scan",
  "client.pickDir": "Choose folder…",
  "client.restore": "Restore…",
  "client.remove": "Remove",
  "client.restoreAt": "Restore {0}",

  "decide.keep": "Keep difference",
  "decide.update": "Update to new version",
  "decide.restore": "Restore",

  "bar.staged": "{0} selected",

  "result.created": "{0} created",
  "result.updated": "{0} updated",
  "result.skipped": "{0} skipped",
  "result.backup": "Backup {0}",
  "pend.unsupported": "{0} unsupported",

  "cov.has": "✓",
  "cov.incoming": "Will write",
  "cov.conflict": "Conflict",
  "cov.confirm": "Needs confirm",
  "cov.deleted": "Deleted",
  "cov.unsupported": "Unsupported",
  "cov.noWrite": "Not writing",
  "cov.none": "—",

  "inbox.baseline": "Baseline",

  "lib.search": "Search",
  "lib.all": "All",
  "lib.pending": "Pending",
  "lib.deleted": "Deleted",
  "lib.deletedAt": "Deleted {0}",

  "set.title": "Settings",
  "set.lang": "Language",
  "set.baseline": "Baseline fork",
  "set.baselineSaved": "Baseline saved",
  "set.storage": "Storage",
  "set.dataDir": "Data directory",
  "set.hubDb": "Hub database",
  "set.backups": "Backups directory",
  "set.backupsCount": "{0} backups",
  "set.engine": "Engine",
  "set.engine.real": "Normal",
  "set.engine.mock": "Demo",
  "set.engine.fallback": "Degraded",
  "set.links": "Link support",
  "set.sym": "Symlink",
  "set.hard": "Hardlink",
  "set.guard": "Game process guard",
  "set.guardHint": "never writes while running",

  "kind.beatmap_set": "beatmap set",
  "kind.collection": "collection",
  "kind.skin": "skin",
  "kind.score": "score",
  "kind.beatmap": "beatmap",
  "kind.key_binding": "key binding",
  "kind.ruleset_setting": "ruleset setting",
  "kind.mod_preset": "Mod Presets",
  "kind.game_ini": "game.ini",

  "toast.saved": "Saved",
  "toast.removed": "Removed",
  "toast.restored": "Restored",
  "toast.assigned": "Assigned",
  "toast.scanDone": "Scan done",
  "toast.registered": "Registered {0}",
};

/** t 文案查表 + {0} 占位替换。 */
export function t(key: string, ...args: (string | number)[]): string {
  const table = lang === "zh" ? zh : en;
  let s = table[key] ?? zh[key] ?? key;
  args.forEach((a, i) => {
    s = s.replace(`{${i}}`, String(a));
  });
  return s;
}

/** trBackend 后端返回文本直译：后端已输出本地化文案时透传。 */
export function trBackend(s: string): string {
  return s;
}

/** kindLabel 实体类短词（收件箱/行内标签）。 */
export function kindLabel(kind: string): string {
  const key = `kind.${kind}`;
  const s = (lang === "zh" ? zh : en)[key] ?? zh[key];
  return s ?? kind;
}
