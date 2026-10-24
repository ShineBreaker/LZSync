// 同步执行页（§6.3）：收集阶段（逐 fork、两步进度）、投放执行前检查面板
//（守卫/备份/计划摘要 + 显式确认）、批级进度（syncProgress 事件实时推送，
// M-2）、结果报告（三段式口径）、从备份还原入口。

import { CompareService, LibraryService, SyncService } from "../mygo";
import type { ProgressEvent } from "../mygo";
import { actionLabel, clear, el, errText, fmtBytes, kindLabel, message, text } from "../ui";

export async function renderRun(root: HTMLElement): Promise<void> {
  clear(root);
  root.append(el("h1", { class: "page-title" }, [text("同步执行")]));

  const statusLine = el("div", { class: "status-line" });

  // ---- 收集阶段 ----
  const collectBox = el("section", { class: "section" }, [
    el("h2", {}, [text("收集（fork → hub）")]),
    el("p", { class: "muted" }, [
      text("只读投影入 hub → 收仓+回链；幂等可重跑。游戏运行中的 fork 会被守卫拦截。"),
    ]),
  ]);
  const collectRows = el("div", { class: "collect-rows" });
  collectBox.append(collectRows);

  // ---- 投放阶段 ----
  const deployBox = el("section", { class: "section" }, [
    el("h2", {}, [text("投放（hub → fork）")]),
  ]);
  const precheck = el("div", { class: "precheck card" });
  const progressBox = el("div", { class: "progress-box hidden" });
  const reportBox = el("div", { class: "report" });
  deployBox.append(precheck, progressBox, reportBox);

  // ---- 还原入口 ----
  const restoreBox = el("section", { class: "section" }, [el("h2", {}, [text("从备份还原")])]);
  const restoreInput = el("input", { class: "input", placeholder: "备份文件路径（见投放报告的备份路径）" });
  const restoreSel = el("select", { class: "input input-s" });
  const restoreBtn = el("button", { class: "btn" }, [text("还原到该 fork")]);
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
      const btn = el("button", { class: "btn" }, [text("收集")]);
      const row = el("div", { class: "collect-row" }, [
        el("span", { class: `fork-dot ${card.ProcessRunning ? "dot-running" : "dot-idle"}` }),
        el("span", { class: "fork-name" }, [text(card.ForkID)]),
        el("span", { class: "muted" }, [text(`库 ${fmtBytes(card.RealmSize)} · files ${fmtBytes(card.FilesSize)}`)]),
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
      .map((s) => `${s.Reason}×${s.Count}`)
      .join("；");
    const adopt = `收仓 ${report.FilesAdopted} 文件（跳过 ${report.FilesSkipped}）`;
    return `收集完成：${ents || "无实体"}；${adopt}${skips ? `；跳过：${skips}` : ""}`;
  }

  /** 执行前检查面板 + 显式确认（不可误触，§6.3）。 */
  async function renderPrecheck(): Promise<void> {
    clear(precheck);
    let plan: Awaited<ReturnType<typeof CompareService.currentPlan>>;
    try {
      plan = await CompareService.currentPlan();
    } catch {
      precheck.append(
        el("p", { class: "muted" }, [text("尚无投放计划——先到「对比预览」页勾选并生成计划。")]),
      );
      return;
    }
    const targetCard = cards.find((c) => c.ForkID === plan.TargetFork);
    const actions = plan.Items.filter((i) => i.Action !== "skip");
    const skips = plan.Items.filter((i) => i.Action === "skip");
    const guardOk = !(targetCard?.ProcessRunning ?? false);

    const items = [
      check("守卫状态", guardOk ? "通过（目标 fork 进程未运行）" : `目标 fork 运行中（${targetCard?.RunningNames.join("、")}），请先关闭游戏`, guardOk),
      check("备份目标", targetCard ? `${targetCard.RealmPath} 写前自动备份（S2）` : "目标库未注册，执行时将被拦截", true),
      check("链接形态", "按 §5.3 自动分级：符号链接 → 硬链接（同卷）→ 复制（谱面集/皮肤/成绩的 files 阶段）", true),
      check("计划摘要", `目标 ${plan.TargetFork} · 动作 ${actions.length} 项 · 计入跳过 ${skips.length} 项`, true),
    ];
    for (const it of items) {
      precheck.append(it);
    }

    const confirm = el("button", { class: "btn btn-danger" }, [text("确认执行投放")]);
    const armed = el("span", { class: "muted hidden" }, [text("再次点击以确认（防误触）")]);
    let armedOnce = false;
    confirm.addEventListener("click", async () => {
      if (!armedOnce) {
        armedOnce = true;
        armed.classList.remove("hidden");
        confirm.textContent = "再次点击确认执行";
        return;
      }
      confirm.disabled = true;
      clear(statusLine);
      showProgress("deploy");
      try {
        const report = await SyncService.deployCurrentPlan();
        renderReport(report);
        statusLine.append(message("ok", "投放结束（见下方报告）"));
      } catch (err) {
        renderReport(undefined, err);
        statusLine.append(message("error", errText(err)));
      } finally {
        hideProgress();
        armedOnce = false;
        armed.classList.add("hidden");
        confirm.textContent = "确认执行投放";
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
      reportBox.append(message("error", `投放未完成：${errText(err)}`));
      return;
    }
    const box = el("div", { class: "card report-card" }, [el("h3", {}, [text("投放报告")])]);
    // applied 按类计数分组（§6.3：按类、按原因分组），组内按动作细分。
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
      return `${kind}：${acts}`;
    });
    box.append(kvRow("已提交批 applied", appliedParts.length > 0 ? appliedParts.join("；") : "无"));
    // R6：delta gimmick 谱面流出告警（§5.4）——显著呈现 + 计数 + 逐条明细
    //（与对比预演的计划标记一致；已同步不阻断，风险在后续编辑保存）。
    const gimmick = report.GimmickWarnings ?? [];
    if (gimmick.length > 0) {
      box.append(
        message(
          "error",
          `⚠ ${gimmick.length} 个谱面集含 delta gimmick section：经非 delta 端编辑保存会静默丢失 gimmick 数据（已照常同步——官方端只读不编辑则安全）`,
        ),
      );
      for (const w of gimmick) {
        box.append(kvRow("告警项", `${kindLabel(String(w.Kind))} ${w.Summary}（${w.ContentKey}）`));
      }
    }
    // 跳过按原因分组计数（SkipCollector 口径，§3.3——不静默吞掉）。
    const skips = report.Skipped ?? [];
    if (skips.length > 0) {
      box.append(kvRow("跳过（按原因）", skips.map((s) => `${s.Reason}×${s.Count}`).join(" · ")));
    }
    const failed = report.FailedBatches ?? [];
    if (failed.length > 0) {
      for (const f of failed) {
        box.append(kvRow(`失败批 #${f.BatchIndex}（整批回滚）`, f.Reason));
      }
    }
    if ((report.NotRun ?? []).length > 0) {
      box.append(kvRow("未执行批清单", `${report.NotRun?.length ?? 0} 项（可从失败处重续）`));
    }
    for (const p of report.Repointed ?? []) {
      box.append(kvRow("执行期改判", `${kindLabel(String(p.Item.Kind))} ${p.Item.ContentKey}：${p.Reason}`));
    }
    if (report.BackupPath) {
      box.append(kvRow("备份路径", report.BackupPath));
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
  const cancelBtn = el("button", { class: "btn btn-danger" }, [text("取消")]);
  cancelBtn.addEventListener("click", () => {
    SyncService.cancel();
    progressLabel.textContent = "已请求取消（批间生效，已提交批保留）……";
  });
  progressBox.append(progressBar, progressLabel, cancelBtn);

  function showProgress(service: string): void {
    progressService = service;
    progressBox.classList.remove("hidden");
    progressFill.style.width = "0%";
    progressLabel.textContent = service === "collect" ? "收集进行中……" : "投放进行中……";
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
    progressLabel.textContent = msg || `${stage} ${current}/${total}`;
  }

  // ---- 还原入口 ----
  restoreBtn.addEventListener("click", async () => {
    const forkID = restoreSel.value;
    const backupPath = restoreInput.value.trim();
    if (!forkID || !backupPath) {
      clear(statusLine);
      statusLine.append(message("info", "还原需要选择 fork 并输入备份路径"));
      return;
    }
    restoreBtn.disabled = true;
    clear(statusLine);
    try {
      await SyncService.restoreBackup(forkID, backupPath);
      statusLine.append(message("ok", `已从 ${backupPath} 还原 ${forkID}（还原前已自动再备份）`));
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
