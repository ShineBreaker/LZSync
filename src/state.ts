// 共享态（DESIGN.md §5）：暂存处置表 + 一轮同步状态机 + 未识别客户端
// 列表。state 模块是各页之间的唯一共享面——页切换重建 DOM，暂存与
// 运行态不丢；订阅者切页时必须退订（页面返回 unsubscribe）。

import { SyncService } from "./mygo";
import type { ProgressEvent, RunPreview, RunReport, Selection, EntityKind } from "./mygo";
import { errText, toast } from "./ui";

// ---------- 暂存处置（§5：跨页存续，底部暂存条驱动） ----------

export type StageAction = "winner" | "keep" | "select";

export interface StagedEntry {
  kind: string;
  contentKey: string;
  label: string;
  action: StageAction;
  winner: string; // action=winner 时的端；其余为 ""
}

const stageMap = new Map<string, StagedEntry>();
const stageSubs = new Set<() => void>();

export function stageKey(kind: string, contentKey: string): string {
  return `${kind}${contentKey}`;
}

export const staged = {
  get size(): number {
    return stageMap.size;
  },
  get(kind: string, contentKey: string): StagedEntry | undefined {
    return stageMap.get(stageKey(kind, contentKey));
  },
  all(): StagedEntry[] {
    return [...stageMap.values()];
  },
  put(e: StagedEntry): void {
    stageMap.set(stageKey(e.kind, e.contentKey), e);
    emitStaged();
  },
  remove(kind: string, contentKey: string): void {
    stageMap.delete(stageKey(kind, contentKey));
    emitStaged();
  },
  clear(): void {
    stageMap.clear();
    emitStaged();
  },
  onChange(fn: () => void): () => void {
    stageSubs.add(fn);
    return () => stageSubs.delete(fn);
  },
};

function emitStaged(): void {
  for (const f of stageSubs) f();
}

// ---------- 未识别客户端（Register 返回 ForkID="" 的本地列表） ----------

export interface PendingClient {
  realmPath: string;
  diskVersion: number;
}

export const pendingClients: PendingClient[] = [];

export function addPendingClient(p: PendingClient): void {
  if (!pendingClients.some((x) => x.realmPath === p.realmPath)) {
    pendingClients.push(p);
  }
}

export function dropPendingClient(realmPath: string): void {
  const i = pendingClients.findIndex((x) => x.realmPath === realmPath);
  if (i >= 0) pendingClients.splice(i, 1);
}

// ---------- 数据刷新面（一轮结束/处置后，页与侧栏重取） ----------

const dataSubs = new Set<() => void>();

export function onData(fn: () => void): () => void {
  dataSubs.add(fn);
  return () => dataSubs.delete(fn);
}

/** bumpData 触发一轮数据刷新（处置/注册/同步完成后各页与侧栏重取）。 */
export function bumpData(): void {
  for (const f of dataSubs) f();
}

function emitData(): void {
  for (const f of dataSubs) f();
}

// ---------- 一轮状态机（§3.2：idle→preparing→ready→committing→done→idle） ----------

export type RunPhase = "idle" | "preparing" | "ready" | "committing" | "done";

export interface LaneView {
  pull: number; // 0..1（收集方向）
  write: number; // 0..1（写入方向）
}

export interface RunView {
  phase: RunPhase;
  source: "all" | "selection";
  total: number; // 预览写入项合计
  preview: RunPreview | null;
  report: RunReport | null;
  lanes: Record<string, LaneView>;
  doneFlash: number; // 完成闪示计数（暂存条「✓ N」）
}

let view: RunView = {
  phase: "idle",
  source: "all",
  total: 0,
  preview: null,
  report: null,
  lanes: {},
  doneFlash: 0,
};

const runSubs = new Set<() => void>();

export function runView(): RunView {
  return view;
}

export function onRun(fn: () => void): () => void {
  runSubs.add(fn);
  return () => runSubs.delete(fn);
}

function set(p: Partial<RunView>): void {
  view = { ...view, ...p };
  for (const f of runSubs) f();
}

// 勾选轮已执行的暂存键（done 后从暂存表摘除）。
let execStaged: StagedEntry[] = [];
let doneTimer = 0;

function lane(forkID: string): LaneView {
  return (view.lanes[forkID] ??= { pull: 0, write: 0 });
}

function previewTotal(p: RunPreview): number {
  return p.Targets.reduce((n, tp) => n + tp.Items, 0);
}

/** busy 期间不可起步。 */
export function canStart(): boolean {
  return view.phase === "idle" || view.phase === "done";
}

/** startAll 库卡主按钮：全量轮。 */
export async function startAll(): Promise<void> {
  if (!canStart()) return;
  set({ phase: "preparing", source: "all", total: 0, preview: null, report: null, lanes: {}, doneFlash: 0 });
  try {
    ready(await SyncService.prepareRun());
  } catch (e) {
    fail(e);
  }
}

/** runStaged 暂存条「同步」：先保留差异（KeepDifference），再勾选轮。 */
export async function runStaged(): Promise<void> {
  if (!canStart()) return;
  const entries = staged.all();
  const keeps = entries.filter((e) => e.action === "keep");
  const rest = entries.filter((e) => e.action !== "keep");
  execStaged = entries;
  if (keeps.length > 0) {
    try {
      await SyncService.keepDifference(
        keeps.map((k) => ({ Kind: k.kind as EntityKind, ContentKey: k.contentKey, Winner: "" })),
      );
    } catch (e) {
      toast("err", errText(e));
      return;
    }
    for (const k of keeps) staged.remove(k.kind, k.contentKey);
    emitData();
  }
  if (rest.length === 0) {
    flashDone(keeps.length);
    return;
  }
  const sel: Selection[] = rest.map((e) => ({
    Kind: e.kind as EntityKind,
    ContentKey: e.contentKey,
    Winner: e.winner,
  }));
  set({ phase: "preparing", source: "selection", total: 0, preview: null, report: null, lanes: {}, doneFlash: 0 });
  try {
    ready(await SyncService.prepareSelected(sel));
  } catch (e) {
    fail(e);
  }
}

function ready(p: RunPreview): void {
  const total = previewTotal(p);
  set({ phase: "ready", preview: p, total });
  if (total === 0) {
    // 无写入项：「已是最新」约 2 秒回 idle。
    window.setTimeout(() => {
      if (view.phase === "ready" && view.total === 0) set({ phase: "idle", preview: null });
    }, 2000);
  }
}

/** confirm 二步确认：ready → committing → commitRun → done。 */
export async function confirmRun(): Promise<void> {
  if (view.phase !== "ready" || !view.preview) return;
  const runID = view.preview.RunID;
  const source = view.source;
  set({ phase: "committing" });
  try {
    const report = await SyncService.commitRun(runID);
    if (report.BaselineError) toast("err", report.BaselineError);
    set({ phase: "done", report });
    afterDone(source);
    emitData();
  } catch (e) {
    fail(e);
  }
}

/** cancel 取消：preparing/committing → SyncService.cancel()；ready → 本地丢弃。 */
export function cancelRun(): void {
  if (view.phase === "ready") {
    set({ phase: "idle", preview: null, total: 0 });
    return;
  }
  if (view.phase === "preparing" || view.phase === "committing") {
    void SyncService.cancel();
  }
}

function fail(e: unknown): void {
  toast("err", errText(e));
  set({ phase: "idle", preview: null, report: null, total: 0, lanes: {} });
}

function flashDone(n: number): void {
  window.clearTimeout(doneTimer);
  set({ phase: "done", doneFlash: n });
  doneTimer = window.setTimeout(() => {
    set({ phase: "idle", doneFlash: 0 });
  }, 1800);
}

function afterDone(source: "all" | "selection"): void {
  if (source !== "selection") return;
  // 勾选轮完成：已执行的暂存项从表摘除（保留「✓ N」闪示后清空）。
  const keys = execStaged;
  execStaged = [];
  window.clearTimeout(doneTimer);
  set({ doneFlash: keys.length });
  doneTimer = window.setTimeout(() => {
    for (const k of keys) staged.remove(k.kind, k.contentKey);
    set({ phase: "idle", doneFlash: 0 });
  }, 1800);
}

// ---------- 连线进度分派（ev.ForkID，DESIGN.md §3.2） ----------

// 阶段 → 轨道比例区间：收集三段 / 写入五段（进度单调不减）。
const PULL_STAGES: Record<string, [number, number]> = {
  project: [0, 1 / 3],
  apply: [1 / 3, 2 / 3],
  adopt: [2 / 3, 1],
};

const WRITE_STAGES: Record<string, [number, number]> = {
  guard: [0, 0.2],
  backup: [0.2, 0.4],
  link: [0.4, 0.6],
  write: [0.6, 0.8],
  verify: [0.8, 1],
};

export function handleProgress(ev: ProgressEvent): void {
  if (ev.ForkID === "") return;
  const frac = ev.Total > 0 ? Math.min(1, ev.Current / ev.Total) : 0;
  const pullRange = PULL_STAGES[ev.Stage];
  if (view.phase === "preparing" && pullRange) {
    const l = lane(ev.ForkID);
    l.pull = Math.max(l.pull, pullRange[0] + (pullRange[1] - pullRange[0]) * frac);
    emit();
    return;
  }
  const writeRange = WRITE_STAGES[ev.Stage];
  if (view.phase === "committing" && writeRange) {
    const l = lane(ev.ForkID);
    l.write = Math.max(l.write, writeRange[0] + (writeRange[1] - writeRange[0]) * frac);
    emit();
  }
}

function emit(): void {
  for (const f of runSubs) f();
}
