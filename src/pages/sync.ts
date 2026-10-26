// 同步页（DESIGN.md §同步页 + design.md §0.4 一轮编排）：
// 确认卡（「写入 N 端 · M 项」+ 跳过端色点+原因 + 待处理计数）→
// 逐端进度行（色点+进度条，跳过端边框转黄）→ 逐端报告（新增/更新/跳过
// 计数、错误）→ 底部还原条。数据页勾选经 setPendingPreview 传入已
// prepare 的预览。

import { t, trBackend } from "../i18n";
import { LibraryService, SyncService } from "../mygo";
import type { ForkRunReport, ProgressEvent, RunPreview } from "../mygo";
import { clear, el, errText, forkDot, kindLabel, message, navigate, text } from "../ui";

// ---- 数据页 → 同步页的手交 / 页间留存 ----

let pendingPreview: RunPreview | null = null;
let wantRun = false;
let lastPreview: RunPreview | null = null;
let lastReport: { preview: RunPreview; forks: ForkRunReport[] } | null = null;

/** setPendingPreview 数据页把 prepareSelected 的预览交给同步页确认。 */
export function setPendingPreview(p: RunPreview | null): void {
  pendingPreview = p;
}

/** requestRun 顶栏「同步」按钮置一次性标志：渲染时触发 prepareRun。
 *  直接切进同步页（侧栏/语言切换）不跑收集——collect 含收仓回链写盘。 */
export function requestRun(): void {
  wantRun = true;
}

// ---- 进度事件分发（main.ts 订阅后调用） ----

let onProgress: ((ev: ProgressEvent) => void) | null = null;

export function detachProgress(): void {
  onProgress = null;
}

export function dispatchProgress(ev: ProgressEvent): void {
  onProgress?.(ev);
}

// ---- 页内态 ----

interface ForkRow {
  fill: HTMLElement;
  label: HTMLElement;
  row: HTMLElement;
}

export async function renderSync(root: HTMLElement): Promise<void> {
  clear(root);
  root.append(el("h1", { class: "page-title" }, [text(t("nav.sync"))]));

  const statusLine = el("div", { class: "status-line" });
  const confirmBox = el("div");
  const progressBox = el("div");
  const reportBox = el("div");
  root.append(confirmBox, progressBox, reportBox, statusLine);

  const forkRows = new Map<string, ForkRow>();
  onProgress = (ev: ProgressEvent) => {
    if (ev.Service !== "run") return;
    const forkID = (ev.Message.split(" ")[0] ?? "").replace(/·$/, "");
    const fr = forkRows.get(forkID);
    const pct = ev.Total > 0 ? Math.round((ev.Current / ev.Total) * 100) : 0;
    if (fr) {
      fr.fill.style.width = `${pct}%`;
      fr.label.textContent = trBackend(ev.Message);
    }
  };

  // ---- 底部还原条 ----
  const restoreBar = el("div", { class: "toolbar" });
  const restoreSel = el("select", { class: "input" }) as HTMLSelectElement;
  const restoreInput = el("input", { class: "input", placeholder: t("sync.restorePath") }) as HTMLInputElement;
  const restoreBtn = el("button", { class: "btn" }, [text(t("sync.restoreBtn"))]);
  restoreBar.append(
    el("span", { class: "page-meta" }, [text(t("sync.restoreFrom"))]),
    restoreSel,
    restoreInput,
    restoreBtn,
  );
  root.append(restoreBar);

  const cards = await LibraryService.list().catch(() => [] as Awaited<ReturnType<typeof LibraryService.list>>);
  for (const c of cards) {
    restoreSel.append(el("option", { value: c.ForkID }, [text(c.ForkID)]));
  }

  restoreBtn.addEventListener("click", async () => {
    const forkID = restoreSel.value;
    const path = restoreInput.value.trim();
    if (!forkID || !path) {
      clear(statusLine);
      statusLine.append(message("info", `${t("sync.restoreTarget")} + ${t("sync.restorePath")}`));
      return;
    }
    restoreBtn.disabled = true;
    clear(statusLine);
    try {
      await SyncService.restoreBackup(forkID, path);
      statusLine.append(message("ok", t("sync.restoreDone")));
    } catch (err) {
      statusLine.append(message("error", errText(err)));
    } finally {
      restoreBtn.disabled = false;
    }
  });

  // ---- 待处理计数（冲突/需确认/不支持） ----
  function pendingChips(p: RunPreview["Pending"]): HTMLElement {
    const box = el("div", { class: "toolbar", style: "gap:8px" });
    const chip = (n: number, label: string, page?: string, filter?: "conflict" | "needsConfirm"): HTMLElement => {
      const node = el("span", { class: `badge${n > 0 ? " warn" : ""}`, style: n > 0 && page ? "cursor:pointer" : "" }, [
        text(`${label} ${n}`),
      ]);
      if (n > 0 && page && filter) {
        node.addEventListener("click", () => {
          navigate(page, filter);
        });
      }
      return node;
    };
    box.append(
      chip(p.Conflicts, t("pend.conflicts"), "conflicts", "conflict"),
      chip(p.NeedsConfirm, t("pend.needsConfirm"), "beatmaps", "needsConfirm"),
      chip(p.Unsupported, t("pend.unsupported")),
      chip(p.Restore, t("pend.restore")),
    );
    return box;
  }

  // ---- 确认卡 ----
  function renderConfirm(p: RunPreview): void {
    clear(confirmBox);
    const card = el("div", { class: "card" });
    const targets = p.Targets.length;
    const items = p.Targets.reduce((n, tp) => n + tp.Items, 0);
    card.append(el("div", { class: "card-title" }, [
      text(t("run.confirm", targets, items)),
    ]));
    // 跳过端：色点 + 端名 + 原因。
    for (const sk of p.Skipped) {
      card.append(el("div", { class: "field" }, [
        el("span", { class: "label", style: "display:flex;align-items:center;gap:8px" }, [
          forkDot(sk.ForkID),
          text(sk.ForkID),
        ]),
        el("span", { class: "value", style: "color:var(--warn)" }, [text(trBackend(sk.Reason))]),
      ]));
    }
    card.append(pendingChips(p.Pending));
    const go = el("button", { class: "btn primary" }, [text(t("run.go"))]);
    const back = el("button", { class: "btn" }, [text(t("run.cancel"))]);
    go.disabled = items === 0;
    go.addEventListener("click", () => {
      void commit(p.RunID);
    });
    back.addEventListener("click", () => {
      pendingPreview = null;
      navigate("beatmaps");
    });
    card.append(el("div", { class: "toolbar", style: "margin-top:10px" }, [go, back]));
    confirmBox.append(card);
  }

  // ---- 逐端进度行 ----
  function renderProgressRows(p: RunPreview): void {
    clear(progressBox);
    forkRows.clear();
    const skipReasons = new Map(p.Skipped.map((s) => [s.ForkID, s.Reason]));
    const forks = new Set<string>();
    for (const tp of p.Targets) forks.add(tp.ForkID);
    for (const sk of p.Skipped) forks.add(sk.ForkID);
    for (const forkID of [...forks].sort()) {
      const row = el("div", { class: "card", style: "display:flex;align-items:center;gap:10px;padding:10px 14px;margin-bottom:6px" });
      const bar = el("div", { class: "progress", style: "flex:1" });
      const fill = el("div", { class: "fill" });
      bar.append(fill);
      const label = el("span", { class: "page-meta" }, [text("")]);
      row.append(forkDot(forkID), el("span", { class: "row-title", style: "font-size:13px" }, [text(forkID)]), bar, label);
      if (skipReasons.has(forkID)) {
        row.style.borderColor = "var(--warn)";
        label.textContent = trBackend(skipReasons.get(forkID) ?? "");
      }
      forkRows.set(forkID, { fill, label, row });
      progressBox.append(row);
    }
    const cancelBtn = el("button", { class: "btn danger" }, [text(t("sync.cancel"))]);
    cancelBtn.addEventListener("click", () => {
      void SyncService.cancel();
    });
    progressBox.append(cancelBtn);
  }

  // ---- 逐端报告 ----
  function renderReport(p: RunPreview, forks: ForkRunReport[], err?: unknown): void {
    clear(reportBox);
    if (err) {
      reportBox.append(el("div", { class: "card" }, [
        el("div", { class: "card-title", style: "color:var(--danger)" }, [text(t("sync.failed"))]),
        el("div", { class: "page-meta" }, [text(errText(err))]),
      ]));
      return;
    }
    for (const fr of forks) {
      const row = el("div", { class: "card", style: "padding:10px 14px;margin-bottom:6px" });
      const head = el("div", { style: "display:flex;align-items:center;gap:10px" }, [
        forkDot(fr.ForkID),
        el("span", { class: "row-title", style: "font-size:13px" }, [text(fr.ForkID)]),
      ]);
      if (fr.Skipped) {
        row.style.borderColor = "var(--warn)";
        head.append(el("span", { class: "page-meta", style: "color:var(--warn)" }, [text(trBackend(fr.SkipReason))]));
      }
      row.append(head);
      if (fr.Error) {
        row.append(el("div", { class: "page-meta", style: "color:var(--danger)" }, [text(trBackend(fr.Error))]));
      }
      if (fr.Deploy) {
        let created = 0;
        let updated = 0;
        for (const a of fr.Deploy.Applied ?? []) {
          if (a.Action === "create") created++;
          else updated++;
        }
        let skipped = 0;
        for (const s of fr.Deploy.Skipped ?? []) skipped += s.Count;
        row.append(el("div", { class: "page-meta" }, [
          text(t("sync.applied", created, updated, skipped)),
        ]));
      }
      if (fr.Collect) {
        const ents = Object.entries(fr.Collect.Entities ?? {})
          .map(([k, n]) => `${kindLabel(k)} ${n}`).join(" · ");
        if (ents) {
          row.append(el("div", { class: "page-meta" }, [text(`${t("run.pulled")} ${ents}`)]));
        }
      }
      reportBox.append(row);
    }
    reportBox.append(pendingChips(p.Pending));
  }

  async function commit(runID: string): Promise<void> {
    clear(confirmBox);
    const p = pendingPreview;
    if (p) renderProgressRows(p);
    try {
      const report = await SyncService.commitRun(runID);
      pendingPreview = null;
      const pv = p ?? { RunID: runID, Targets: [], Skipped: [], Pending: report.Pending };
      lastPreview = pv;
      lastReport = { preview: pv, forks: report.Forks };
      renderReport(pv, report.Forks);
    } catch (err) {
      renderReport(p ?? { RunID: runID, Targets: [], Skipped: [], Pending: { Conflicts: 0, NeedsConfirm: 0, Restore: 0, Unsupported: 0, ByKind: {} } }, [], err);
      statusLine.append(message("error", errText(err)));
    }
  }

  // ---- 入口：数据页带来的预览优先；顶栏按钮的一次性标志才触发
  // prepareRun（进入本页本身不跑收集——收集含收仓回链写盘）；
  // 否则回显上一次报告/预览，都没有就只有还原条。 ----
  if (pendingPreview) {
    renderConfirm(pendingPreview);
    return;
  }
  if (wantRun) {
    wantRun = false; // 一次性消费
    clear(statusLine);
    statusLine.append(message("info", t("run.preparing")));
    try {
      const p = await SyncService.prepareRun();
      pendingPreview = p;
      lastPreview = p;
      clear(statusLine);
      renderConfirm(p);
    } catch (err) {
      clear(statusLine);
      statusLine.append(message("error", errText(err)));
    }
    return;
  }
  if (lastReport) {
    renderReport(lastReport.preview, lastReport.forks);
    return;
  }
  if (lastPreview) {
    pendingPreview = lastPreview;
    renderConfirm(lastPreview);
  }
}
