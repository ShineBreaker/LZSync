// 同步页（DESIGN.md §同步页）：三步条（拉取→同步到→还原）。
// 左列=各客户端拉取入口；右列=同步前检查→进度→报告；底部=还原条。

import { t, trBackend } from "../i18n";
import { CompareService, LibraryService, SyncService } from "../mygo";
import type { ProgressEvent } from "../mygo";
import { actionLabel, clear, el, errText, fmtBytes, forkDot, kindLabel, message, text } from "../ui";

export async function renderSync(root: HTMLElement): Promise<void> {
  clear(root);
  root.append(el("h1", { class: "page-title" }, [text(t("sync.title"))]));

  const statusLine = el("div", { class: "status-line" });

  // ---- 三步条 ----
  const steps = el("div", { class: "steps" }, [
    step("1", t("sync.step1"), false),
    el("div", { class: "step-link" }),
    step("2", t("sync.step2"), true),
    el("div", { class: "step-link" }),
    step("3", t("sync.step3"), false),
  ]);
  root.append(steps);

  function step(n: string, label: string, active: boolean): HTMLElement {
    const s = el("div", { class: `step${active ? " active" : " done"}` });
    s.append(el("div", { class: "dot" }, [text(n)]), el("span", {}, [text(label)]));
    return s;
  }

  // ---- 双列：左拉取 / 右同步到 ----
  const cols = el("div", { class: "grid-2" });
  root.append(cols);

  // 左列
  const left = el("div");
  left.append(el("div", { class: "card-title" }, [text(t("sync.step1"))]));
  const pullBox = el("div");
  left.append(pullBox);
  cols.append(left);

  // 右列
  const right = el("div");
  right.append(el("div", { class: "card-title" }, [text(t("sync.step2"))]));
  const precheck = el("div", { class: "card" });
  const progressBox = el("div", { class: "hidden" });
  const reportBox = el("div");
  right.append(precheck, progressBox, reportBox);
  cols.append(right);

  // ---- 底部还原条 ----
  const restoreBar = el("div", { class: "toolbar" });
  const restoreSel = el("select", { class: "input" }) as HTMLSelectElement;
  const restoreInput = el("input", { class: "input", placeholder: t("sync.restorePath") });
  const restoreBtn = el("button", { class: "btn" }, [text(t("sync.restoreBtn"))]);
  restoreBar.append(
    el("span", { class: "page-meta" }, [text(t("sync.restoreFrom"))]),
    restoreSel,
    restoreInput,
    restoreBtn,
  );
  root.append(restoreBar, statusLine);

  // ---- 数据 ----
  const cards = await LibraryService.list();
  for (const c of cards) {
    restoreSel.append(el("option", { value: c.ForkID }, [text(c.ForkID)]));
  }

  // 拉取行
  function renderPullRows(): void {
    clear(pullBox);
    for (const c of cards) {
      const btn = el("button", { class: "btn" }, [text(t("sync.pullBtn"))]);
      btn.disabled = c.ProcessRunning;
      const row = el("div", { class: "card" });
      row.style.display = "flex";
      row.style.alignItems = "center";
      row.style.gap = "10px";
      row.style.padding = "10px 14px";
      row.style.marginBottom = "6px";
      row.append(
        forkDot(c.ForkID),
        el("span", { class: "row-title", style: "font-size:13px" }, [text(c.ForkID)]),
        el("span", { class: "spacer" }),
        el("span", { class: "page-meta" }, [text(fmtBytes(c.RealmSize + c.FilesSize))]),
        btn,
      );
      if (c.ProcessRunning) {
        row.style.borderColor = "var(--warn)";
      }
      btn.addEventListener("click", async () => {
        btn.disabled = true;
        clear(statusLine);
        try {
          const report = await SyncService.collect(c.ForkID);
          const ents = Object.entries(report.Entities ?? {}).map(([k, n]) => `${kindLabel(k)} ${n}`).join(" · ");
          const skips = (report.Skips ?? []).map((s) => `${trBackend(s.Reason)} ×${s.Count}`).join(", ");
          statusLine.append(message("ok", `${c.ForkID}: ${ents || "—"}${skips ? ` · ${skips}` : ""}`));
        } catch (err) {
          statusLine.append(message("error", errText(err)));
        } finally {
          btn.disabled = false;
        }
      });
      pullBox.append(row);
    }
  }

  // 同步前检查
  let currentPlanTarget = "";
  async function renderPrecheck(): Promise<void> {
    clear(precheck);
    let plan: Awaited<ReturnType<typeof CompareService.currentPlan>>;
    try {
      plan = await CompareService.currentPlan();
    } catch {
      currentPlanTarget = "";
      precheck.append(el("p", { class: "page-meta" }, [text(t("sync.noPlan"))]));
      return;
    }
    currentPlanTarget = plan.TargetFork;
    const targetCard = cards.find((c) => c.ForkID === plan.TargetFork);
    const actions = plan.Items.filter((i) => i.Action !== "skip");
    const skips = plan.Items.filter((i) => i.Action === "skip");
    const guardOk = !(targetCard?.ProcessRunning ?? false);

    const rows: [string, boolean][] = [
      [t("sync.checkGuard"), guardOk],
      [t("sync.checkBackup"), Boolean(targetCard)],
      [t("sync.checkPlan", actions.length + skips.length), true],
    ];
    for (const [label, ok] of rows) {
      precheck.append(
        el("div", { class: "field" }, [
          el("span", { class: "label", style: `color:${ok ? "var(--ok)" : "var(--danger)"}` }, [text(ok ? "✓" : "✗")]),
          el("span", { class: "value" }, [text(label)]),
        ]),
      );
    }
    if (targetCard && targetCard.ProcessRunning) {
      precheck.append(el("div", { class: "message warn" }, [text(`${targetCard.ForkID} ${t("clients.running")}`)]));
    }
    if (targetCard && !targetCard.ProcessRunning) {
      precheck.append(el("div", { class: "page-meta" }, [text(`${t("sync.backup")} ${targetCard.RealmPath}`)]));
    }

    const btn = el("button", { class: "btn primary" }, [text(t("sync.pushBtn"))]);
    btn.disabled = !guardOk || actions.length === 0;
    let armed = false;
    btn.addEventListener("click", async () => {
      if (!armed) {
        armed = true;
        btn.textContent = `${t("sync.pushBtn")}?`;
        return;
      }
      btn.disabled = true;
      clear(statusLine);
      showProgress();
      try {
        const report = await SyncService.deployCurrentPlan();
        renderReport(report);
        statusLine.append(message("ok", t("sync.done")));
      } catch (err) {
        renderReport(undefined, err);
        statusLine.append(message("error", errText(err)));
      } finally {
        hideProgress();
        armed = false;
        btn.textContent = t("sync.pushBtn");
        btn.disabled = false;
        await renderPrecheck();
      }
    });
    precheck.append(el("div", { style: "margin-top:12px" }, [btn]));
  }

  // 报告
  function renderReport(report?: Awaited<ReturnType<typeof SyncService.deployCurrentPlan>>, err?: unknown): void {
    clear(reportBox);
    const box = el("div", { class: "card" });
    if (!report) {
      box.append(el("div", { class: "card-title", style: "color:var(--danger)" }, [text(t("sync.failed"))]));
      box.append(el("div", { class: "page-meta" }, [text(errText(err))]));
      reportBox.append(box);
      return;
    }
    const applied = report.Applied ?? [];
    const byAction = new Map<string, number>();
    for (const a of applied) {
      byAction.set(a.Action, (byAction.get(a.Action) ?? 0) + 1);
    }
    const parts = [...byAction.entries()].map(([act, n]) => `${actionLabel(act)} ${n}`);
    const skips = (report.Skipped ?? []).map((s) => `${trBackend(s.Reason)} ×${s.Count}`).join(", ");
    box.append(
      el("div", { class: "card-title", style: "color:var(--ok)" }, [text(`${t("sync.done")} · ${plan2target()}`)]),
      el("div", { class: "page-meta" }, [text(parts.length > 0 ? parts.join(" · ") : t("sync.applied", 0, 0, 0))]),
      ...(skips ? [el("div", { class: "page-meta" }, [text(skips)])] : []),
      ...(report.BackupPath ? [el("div", { class: "page-meta" }, [text(`${t("sync.backup")} ${report.BackupPath}`)])] : []),
    );
    reportBox.append(box);
  }

  function plan2target(): string {
    return currentPlanTarget;
  }

  // ---- 进度 ----
  let progressService = "";
  const progressBar = el("div", { class: "progress" });
  const progressFill = el("div", { class: "fill" });
  const progressLabel = el("div", { class: "page-meta" });
  progressBar.append(progressFill);
  const cancelBtn = el("button", { class: "btn danger" }, [text(t("sync.cancel"))]);
  cancelBtn.addEventListener("click", () => {
    SyncService.cancel();
    progressLabel.textContent = t("sync.running");
  });
  progressBox.append(progressBar, progressLabel, cancelBtn);

  function showProgress(): void {
    progressService = "deploy";
    progressBox.classList.remove("hidden");
    progressFill.style.width = "0%";
    progressLabel.textContent = t("sync.running");
  }
  function hideProgress(): void {
    progressBox.classList.add("hidden");
    progressService = "";
  }

  onProgress = (ev: ProgressEvent) => {
    if (progressService !== ev.Service) return;
    const pct = ev.Total > 0 ? Math.round((ev.Current / ev.Total) * 100) : 0;
    progressFill.style.width = `${pct}%`;
    progressLabel.textContent = trBackend(ev.Message) || `${ev.Stage} ${ev.Current}/${ev.Total}`;
  };

  // ---- 还原 ----
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

  renderPullRows();
  await renderPrecheck();
}

// 进度事件分发（main.ts 订阅后调用）
let onProgress: ((ev: ProgressEvent) => void) | null = null;

export function detachProgress(): void {
  onProgress = null;
}

export function dispatchProgress(ev: ProgressEvent): void {
  onProgress?.(ev);
}
