// 同步执行页（§6.3）：收集阶段（逐 fork、两步进度）、投放执行前检查面板
//（守卫/备份/计划摘要 + 显式确认）、批级进度（syncProgress 事件实时推送，
// M-2）、结果报告（三段式口径）、从备份还原入口。

import { t, trBackend } from "../i18n";
import { CompareService, LibraryService, SyncService } from "../mygo";
import type { ProgressEvent } from "../mygo";
import { actionLabel, clear, el, errText, fmtBytes, kindLabel, message, text } from "../ui";

export async function renderRun(root: HTMLElement): Promise<void> {
  clear(root);
  root.append(el("h1", { class: "page-title" }, [text(t("nav.run"))]));

  const statusLine = el("div", { class: "status-line" });

  // ---- 收集阶段 ----
  const collectBox = el("section", { class: "section" }, [
    el("h2", {}, [text(t("run.collectTitle"))]),
    el("p", { class: "muted" }, [
      text(t("run.collectDesc")),
    ]),
  ]);
  const collectRows = el("div", { class: "collect-rows" });
  collectBox.append(collectRows);

  // ---- 投放阶段 ----
  const deployBox = el("section", { class: "section" }, [
    el("h2", {}, [text(t("run.deployTitle"))]),
  ]);
  const precheck = el("div", { class: "precheck card" });
  const progressBox = el("div", { class: "progress-box hidden" });
  const reportBox = el("div", { class: "report" });
  deployBox.append(precheck, progressBox, reportBox);

  // ---- 还原入口 ----
  const restoreBox = el("section", { class: "section" }, [el("h2", {}, [text(t("run.restoreTitle"))])]);
  const restoreInput = el("input", { class: "input", placeholder: t("run.restorePlaceholder") });
  const restoreSel = el("select", { class: "input input-s" });
  const restoreBtn = el("button", { class: "btn" }, [text(t("run.restoreBtn"))]);
  restoreBox.append(el("div", { class: "toolbar" }, [restoreSel, restoreInput, restoreBtn]));

  root.append(collectBox, deployBox, restoreBox, statusLine);

  // 页面挂载期间接收进度事件（main.ts 订阅后分发）。
  onProgress = (ev: ProgressEvent) => {
    renderProgress(ev.Service, ev.Stage, ev.Current, ev.Total, ev.Message);
  };

  const cards = await LibraryService.list();
  for (const card of cards) {
    restoreSel.append(el("option", { value: card.ForkID }, [text(card.ForkID)]));
  }
  await renderCollectRows();
  await renderPrecheck();

  async function renderCollectRows(): Promise<void> {
    clear(collectRows);
    for (const card of cards) {
      const btn = el("button", { class: "btn" }, [text(t("run.collectBtn"))]);
      const row = el("div", { class: "collect-row" }, [
        el("span", { class: `fork-dot ${card.ProcessRunning ? "dot-running" : "dot-idle"}` }),
        el("span", { class: "fork-name" }, [text(card.ForkID)]),
        el("span", { class: "muted" }, [text(t("run.cardStats", fmtBytes(card.RealmSize), fmtBytes(card.FilesSize)))]),
        el("span", { class: "spacer" }),
        btn,
      ]);
      btn.addEventListener("click", async () => {
        btn.disabled = true;
        clear(statusLine);
        showProgress("collect");
        try {
          const report = await SyncService.collect(card.ForkID);
          clear(statusLine);
          statusLine.append(message("ok", collectSummary(report)));
        } catch (err) {
          clear(statusLine);
          statusLine.append(message("error", errText(err)));
        } finally {
          hideProgress();
          await renderCollectRows();
          await renderPrecheck();
        }
      });
      collectRows.append(row);
    }
  }

  function collectSummary(report: Awaited<ReturnType<typeof SyncService.collect>>): string {
    const ents = Object.entries(report.Entities ?? {})
      .map(([kind, n]) => `${kindLabel(kind)} ${n}`)
      .join(" · ");
    const skips = (report.Skips ?? [])
      .map((s) => `${trBackend(s.Reason)} ×${s.Count}`)
      .join(", ");
    const skipStr = skips ? `${t("run.skippedPrefix")}${skips}` : "";
    return t("run.collectDone", ents || t("run.noEntities"), report.FilesAdopted, report.FilesSkipped, skipStr);
  }

  /** 执行前检查面板 + 显式确认（不可误触，§6.3）。 */
  async function renderPrecheck(): Promise<void> {
    clear(precheck);
    let plan: Awaited<ReturnType<typeof CompareService.currentPlan>>;
    try {
      plan = await CompareService.currentPlan();
    } catch {
      precheck.append(
        el("p", { class: "muted" }, [text(t("run.noPlan"))]),
      );
      return;
    }
    const targetCard = cards.find((c) => c.ForkID === plan.TargetFork);
    const actions = plan.Items.filter((i) => i.Action !== "skip");
    const skips = plan.Items.filter((i) => i.Action === "skip");
    const guardOk = !(targetCard?.ProcessRunning ?? false);

    const items = [
      check(t("run.checkGuard"), guardOk ? t("run.guardOk") : t("run.guardFail", targetCard?.RunningNames.join(", ") ?? ""), guardOk),
      check(t("run.checkBackup"), targetCard ? t("run.backupOk", targetCard.RealmPath) : t("run.backupUnreg"), Boolean(targetCard)),
      check(t("run.checkPlan"), t("run.planSummary", plan.TargetFork, actions.length, skips.length), true),
    ];
    for (const it of items) {
      precheck.append(it);
    }

    const confirm = el("button", { class: "btn btn-danger" }, [text(t("run.deployBtn"))]);
    const armed = el("span", { class: "muted hidden" }, [text(t("run.deployArmedHint"))]);
    let armedOnce = false;
    confirm.addEventListener("click", async () => {
      if (!armedOnce) {
        armedOnce = true;
        armed.classList.remove("hidden");
        confirm.textContent = t("run.deployConfirmBtn");
        return;
      }
      confirm.disabled = true;
      clear(statusLine);
      showProgress("deploy");
      try {
        const report = await SyncService.deployCurrentPlan();
        renderReport(report);
        statusLine.append(message("ok", t("run.deployDone")));
      } catch (err) {
        renderReport(undefined, err);
        statusLine.append(message("error", errText(err)));
      } finally {
        hideProgress();
        armedOnce = false;
        armed.classList.add("hidden");
        confirm.textContent = t("run.deployBtn");
        confirm.disabled = false;
        await renderPrecheck();
      }
    });
    precheck.append(el("div", { class: "precheck-actions" }, [armed, confirm]));
  }

  function check(name: string, desc: string, ok: boolean): HTMLElement {
    return el("div", { class: "check-row" }, [
      el("span", { class: `check-mark ${ok ? "check-ok" : "check-bad"}` }, [text(ok ? "✓" : "✗")]),
      el("span", { class: "check-name" }, [text(name)]),
      el("span", { class: "muted" }, [text(desc)]),
    ]);
  }

  /** 结果报告（§6.3 三段式 + 改判 + 备份）。 */
  function renderReport(
    report?: Awaited<ReturnType<typeof SyncService.deployCurrentPlan>>,
    err?: unknown,
  ): void {
    clear(reportBox);
    if (!report) {
      reportBox.append(message("error", t("run.deployFailed", errText(err))));
      return;
    }
    const box = el("div", { class: "card report-card" }, [el("h3", {}, [text(t("run.reportTitle"))])]);
    const appliedByKind = new Map<string, Map<string, number>>();
    for (const a of report.Applied ?? []) {
      const kind = kindLabel(String(a.Kind));
      const act = actionLabel(String(a.Action));
      const byAct = appliedByKind.get(kind) ?? new Map<string, number>();
      byAct.set(act, (byAct.get(act) ?? 0) + 1);
      appliedByKind.set(kind, byAct);
    }
    const appliedParts = [...appliedByKind.entries()].map(([kind, byAct]) => {
      const acts = [...byAct.entries()].map(([act, n]) => `${act} ${n}`).join(" · ");
      return `${kind}: ${acts}`;
    });
    box.append(kvRow(t("run.reportApplied"), appliedParts.length > 0 ? appliedParts.join("; ") : t("run.reportNone")));

    const gimmick = report.GimmickWarnings ?? [];
    if (gimmick.length > 0) {
      box.append(
        message(
          "error",
          t("run.gimmickWarn", gimmick.length),
        ),
      );
      for (const w of gimmick) {
        box.append(kvRow(t("run.gimmickItem"), `${kindLabel(String(w.Kind))} ${trBackend(w.Summary)} (${w.ContentKey})`));
      }
    }
    const skips = report.Skipped ?? [];
    if (skips.length > 0) {
      box.append(kvRow(t("run.reportSkipped"), skips.map((s) => `${trBackend(s.Reason)} ×${s.Count}`).join(" · ")));
    }
    const failed = report.FailedBatches ?? [];
    if (failed.length > 0) {
      for (const f of failed) {
        box.append(kvRow(t("run.reportFailedBatch", f.BatchIndex), trBackend(f.Reason)));
      }
    }
    if ((report.NotRun ?? []).length > 0) {
      box.append(kvRow(t("run.reportNotRun"), t("run.reportNotRunCount", report.NotRun?.length ?? 0)));
    }
    for (const p of report.Repointed ?? []) {
      box.append(kvRow(t("run.reportRepointed"), `${kindLabel(String(p.Item.Kind))} ${p.Item.ContentKey}: ${trBackend(p.Reason)}`));
    }
    if (report.BackupPath) {
      box.append(kvRow(t("run.reportBackup"), report.BackupPath));
      restoreInput.value = report.BackupPath;
    }
    reportBox.append(box);
  }

  function kvRow(name: string, desc: string): HTMLElement {
    return el("div", { class: "check-row" }, [
      el("span", { class: "check-name" }, [text(name)]),
      el("span", { class: "muted" }, [text(desc)]),
    ]);
  }

  // ---- 进度区（syncProgress 事件驱动，§6.3 批级进度） ----
  let progressService = "";
  const progressBar = el("div", { class: "progress-track" }, []);
  const progressFill = el("div", { class: "progress-fill" });
  const progressLabel = el("div", { class: "progress-label muted" });
  progressBar.append(progressFill);
  const cancelBtn = el("button", { class: "btn btn-danger" }, [text(t("run.cancelBtn"))]);
  cancelBtn.addEventListener("click", () => {
    SyncService.cancel();
    progressLabel.textContent = t("run.cancelling");
  });
  progressBox.append(progressBar, progressLabel, cancelBtn);

  function showProgress(service: string): void {
    progressService = service;
    progressBox.classList.remove("hidden");
    progressFill.style.width = "0%";
    progressLabel.textContent = service === "collect" ? t("run.collecting") : t("run.deploying");
  }

  function hideProgress(): void {
    progressBox.classList.add("hidden");
    progressService = "";
  }

  function renderProgress(service: string, stage: string, current: number, total: number, msg: string): void {
    if (!progressService || progressService !== service) {
      return;
    }
    const pct = total > 0 ? Math.round((current / total) * 100) : 0;
    progressFill.style.width = `${pct}%`;
    progressLabel.textContent = msg ? trBackend(msg) : `${stage} ${current}/${total}`;
  }

  // ---- 还原入口 ----
  restoreBtn.addEventListener("click", async () => {
    const forkID = restoreSel.value;
    const backupPath = restoreInput.value.trim();
    if (!forkID || !backupPath) {
      clear(statusLine);
      statusLine.append(message("info", t("run.restoreNeedInput")));
      return;
    }
    restoreBtn.disabled = true;
    clear(statusLine);
    try {
      await SyncService.restoreBackup(forkID, backupPath);
      statusLine.append(message("ok", t("run.restoreDone", backupPath, forkID)));
    } catch (err) {
      statusLine.append(message("error", errText(err)));
    } finally {
      restoreBtn.disabled = false;
    }
  });
}

/** 进度事件回调槽（main.ts 订阅 events.syncProgress 后分发到当前页；
 * 换页时由 main.ts 置空，旧页闭包自然失效）。 */
let onProgress: ((ev: ProgressEvent) => void) | null = null;

/** 换页时解除当前页的进度回调（main.ts 调用）。 */
export function detachProgress(): void {
  onProgress = null;
}

/** main.ts 调用：把进度事件分发给当前执行页。 */
export function dispatchProgress(ev: ProgressEvent): void {
  onProgress?.(ev);
}
