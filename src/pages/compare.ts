// 对比预览页（§6.2）：模式选择（hub→fork 投放预演 / fork↔fork 快照对比）、
// 类型 Tab（谱面集/难度/收藏/皮肤/成绩）与分类筛选、「需用户确认」分组
//（元数据弱键，默认不勾选）、差异列表（分类徽标含「恢复（已删除）」与
// 强/弱键标注、行级勾选、冲突字段展开）、底部统计条（N 条同步 / M 条
// 跳过·k 类原因）、生成投放计划。

import { CompareService, ExportService, LibraryService } from "../mygo";
import { categoryClass, categoryLabel, clear, el, errText, fmtMillis, kindLabel, message, text } from "../ui";

/** 行的运行态（勾选状态脱离 DefaultChecked 后由 UI 持有）。 */
interface RowState {
  key: string;
  checked: boolean;
}

/** 类型 Tab（§6.2 数据面五类 + 全部；「全部」默认不含成绩——成绩量大且
 * 常需单独审视，对齐 EZRealmSync 的 EntityKindFilter.All 语义）。 */
const KIND_TABS = [
  { id: "all", label: "全部" },
  { id: "beatmap_set", label: "谱面集" },
  { id: "beatmap", label: "难度" },
  { id: "collection", label: "收藏夹" },
  { id: "skin", label: "皮肤" },
  { id: "score", label: "成绩" },
] as const;

/** 分类筛选（纯结果筛选，§6.2）。 */
const CAT_FILTERS = [
  { id: "all", label: "全部" },
  { id: "create", label: "新增" },
  { id: "conflict", label: "仅冲突" },
  { id: "version-update", label: "仅版本更新" },
  { id: "restore", label: "恢复（已删除）" },
  { id: "target-only", label: "目标独有" },
] as const;

/** 有容器形态的类型（§7-M3-5 路线④：谱面集 .osz / 皮肤 .osk / 成绩 .osr）。 */
const EXPORTABLE_KINDS = new Set(["beatmap_set", "skin", "score"]);

type DiffRowView = Awaited<ReturnType<typeof CompareService.previewDeploy>>[number];

export async function renderCompare(root: HTMLElement): Promise<void> {
  clear(root);
  root.append(el("h1", { class: "page-title" }, [text("对比预览")]));

  // 页面状态。
  let mode: "preview" | "forks" = "preview";
  let kindFilter = "all";
  let catFilter = "all";
  let rows: DiffRowView[] = [];
  const checked = new Map<string, boolean>();

  // 控件。
  const modeSel = el("select", { class: "input input-s" }, [
    el("option", { value: "preview" }, [text("hub 真相 → 单 fork 投放预演")]),
    el("option", { value: "forks" }, [text("fork ↔ fork 快照对比")]),
  ]);
  const sourceSel = el("select", { class: "input input-s" });
  const targetSel = el("select", { class: "input input-s" });
  const queryBtn = el("button", { class: "btn btn-primary" }, [text("查询差异")]);
  const hint = el("span", { class: "muted" }, [text("全部查询走 hub，页面不触碰任何 fork 库。")]);
  const controls = el("div", { class: "toolbar" }, [modeSel, sourceSel, targetSel, queryBtn, hint]);
  const statusLine = el("div", { class: "status-line" });
  const tabsBar = el("div", { class: "tabs" });
  const filtersBar = el("div", { class: "filters" });
  const listBox = el("div", { class: "diff-list" });
  const statsBar = el("div", { class: "stats-bar" });
  const buildBtn = el("button", { class: "btn btn-primary" }, [text("生成投放计划")]);
  root.append(controls, statusLine, tabsBar, filtersBar, listBox, el("div", { class: "bottom-bar" }, [statsBar, buildBtn]));

  // fork 选择器数据。
  const cards = await LibraryService.list();
  for (const card of cards) {
    sourceSel.append(el("option", { value: card.ForkID }, [text(card.ForkID)]));
    targetSel.append(el("option", { value: card.ForkID }, [text(card.ForkID)]));
  }
  if (cards.length > 1) {
    const second = cards[1];
    if (second) targetSel.value = second.ForkID;
  }
  updateSourceVis();

  modeSel.addEventListener("change", updateSourceVis);

  function updateSourceVis(): void {
    mode = modeSel.value === "forks" ? "forks" : "preview";
    sourceSel.style.display = mode === "forks" ? "" : "none";
  }

  queryBtn.addEventListener("click", async () => {
    queryBtn.disabled = true;
    clear(statusLine);
    statusLine.append(message("info", "查询中……"));
    try {
      const target = targetSel.value;
      if (mode === "forks") {
        rows = await CompareService.compareForks(sourceSel.value, target);
      } else {
        // 投放预演 = 预演行 + 恢复组（去重合并；预演行已含 tombstone 命中项）。
        const [preview, deleted] = await Promise.all([
          CompareService.previewDeploy(target),
          CompareService.deletedEntries(target),
        ]);
        const seen = new Set(preview.map((r) => `${r.Kind}\u0000${r.ContentKey}`));
        rows = [...preview];
        for (const d of deleted) {
          const key = `${d.Kind}\u0000${d.ContentKey}`;
          if (!seen.has(key)) {
            rows.push(d);
          }
        }
      }
      checked.clear();
      for (const r of rows) {
        checked.set(rowKey(r), r.DefaultChecked);
      }
      clear(statusLine);
      renderTabs();
      renderFilters();
      renderList();
    } catch (err) {
      clear(statusLine);
      statusLine.append(message("error", errText(err)));
      rows = [];
      renderTabs();
      renderFilters();
      renderList();
    } finally {
      queryBtn.disabled = false;
    }
  });

  buildBtn.addEventListener("click", async () => {
    const sel: { Kind: string; ContentKey: string }[] = [];
    for (const r of rows) {
      if (checked.get(rowKey(r)) === true) {
        sel.push({ Kind: String(r.Kind), ContentKey: r.ContentKey });
      }
    }
    if (sel.length === 0) {
      clear(statusLine);
      statusLine.append(message("info", "未勾选任何条目（底部统计条见明细）"));
      return;
    }
    buildBtn.disabled = true;
    clear(statusLine);
    try {
      const plan = await CompareService.buildPlan(targetSel.value, sel);
      const actions = plan.Items.filter((i) => i.Action !== "skip").length;
      const skips = plan.Items.length - actions;
      statusLine.append(
        message("ok", `计划已生成并持久化：目标 ${plan.TargetFork} · 动作 ${actions} 项 · 计入跳过 ${skips} 项（含原因）。到「同步执行」页执行。`),
      );
    } catch (err) {
      statusLine.append(message("error", errText(err)));
    } finally {
      buildBtn.disabled = false;
    }
  });

  /** 行级「导出容器」入口（§7-M3-5 路线④降级）：hub 真相 → 官方兼容
   * 容器 → 系统默认打开方式交给目标 fork 客户端导入；不触碰目标 .realm。 */
  function renderExportBtn(r: DiffRowView): HTMLElement {
    const btn = el("button", {
      class: "btn btn-ghost btn-sm",
      title: "生成 .osz/.osk/.osr 到临时目录并按系统默认打开方式导入（路线④降级，不写目标库）",
    }, [text("导出容器")]);
    btn.addEventListener("click", async () => {
      btn.disabled = true;
      try {
        const res = await ExportService.exportContainers(targetSel.value, String(r.Kind), [r.ContentKey]);
        clear(statusLine);
        const where = `已生成 ${res.Paths.length} 个容器（目标 ${targetSel.value}）：${res.Paths.join("；")}`;
        statusLine.append(message(res.Note ? "info" : "ok", res.Note ? `${where}。拉起提示：${res.Note}` : `${where}，已交给系统默认程序导入。`));
      } catch (err) {
        clear(statusLine);
        statusLine.append(message("error", errText(err)));
      } finally {
        btn.disabled = false;
      }
    });
    return btn;
  }

  function rowKey(r: DiffRowView): string {
    return `${r.Kind}\u0000${r.ContentKey}`;
  }

  function visibleRows(): DiffRowView[] {
    return rows.filter((r) => {
      const kind = String(r.Kind);
      if (kindFilter === "all") {
        if (kind === "score") return false; // 「全部」默认不含成绩（§6.2）
      } else if (kind !== kindFilter) {
        return false;
      }
      if (catFilter !== "all" && String(r.Category) !== catFilter) return false;
      return true;
    });
  }

  function renderTabs(): void {
    clear(tabsBar);
    for (const tab of KIND_TABS) {
      const btn = el("button", { class: `tab ${kindFilter === tab.id ? "tab-active" : ""}` }, [text(tab.label)]);
      btn.addEventListener("click", () => {
        kindFilter = tab.id;
        renderTabs();
        renderList();
      });
      tabsBar.append(btn);
    }
  }

  function renderFilters(): void {
    clear(filtersBar);
    for (const f of CAT_FILTERS) {
      const btn = el("button", { class: `chip ${catFilter === f.id ? "chip-active" : ""}` }, [text(f.label)]);
      btn.addEventListener("click", () => {
        catFilter = f.id;
        renderFilters();
        renderList();
      });
      filtersBar.append(btn);
    }
  }

  function renderList(): void {
    clear(listBox);
    if (rows.length === 0) {
      listBox.append(el("p", { class: "muted" }, [text("选择模式与 fork 后点击「查询差异」。")]));
      renderStats();
      return;
    }
    if (kindFilter === "beatmap") {
      // 难度层不做独立版本对账（§4.2：难度生命周期挂在可软删的父集上
      // 整集管理），对账面不产独立差异行——空态说明而非伪数据。
      listBox.append(
        el("p", { class: "muted" }, [text("难度差异随谱面集整集管理（谱面集的版本替换即整集替换，§4.2），无独立差异行。")]),
      );
      renderStats();
      return;
    }
    const visible = visibleRows();
    if (visible.length === 0) {
      listBox.append(el("p", { class: "muted" }, [text("当前筛选无匹配行。")]));
      renderStats();
      return;
    }
    // 「需用户确认」分组（§6.2）：元数据弱键「疑似」版本更新集中呈现，
    // 默认不勾选；勾选即三选一拍板中的「替换」（不勾选 = 忽略）。
    const needsConfirm = visible.filter((r) => String(r.Evidence) === "weak");
    const normal = visible.filter((r) => String(r.Evidence) !== "weak");
    if (needsConfirm.length > 0) {
      listBox.append(
        el("div", { class: "group-title" }, [text("需用户确认（默认不勾选）")]),
        el("p", { class: "muted" }, [text("元数据弱键「疑似」版本更新——勾选即拍板按版本替换执行，不勾选视为忽略（§5.4 三选一）。")]),
      );
      for (const r of needsConfirm) {
        listBox.append(renderRow(r));
      }
      if (normal.length > 0) {
        listBox.append(el("div", { class: "group-title" }, [text("其余差异")]));
      }
    }
    for (const r of normal) {
      listBox.append(renderRow(r));
    }
    renderStats();
  }

  function renderRow(r: DiffRowView): HTMLElement {
    const key = rowKey(r);
    const cb = el("input", { type: "checkbox", class: "row-check" }) as HTMLInputElement;
    cb.checked = checked.get(key) === true;
    cb.addEventListener("change", () => {
      checked.set(key, cb.checked);
      renderStats();
    });

    const badges = [el("span", { class: categoryClass(String(r.Category)) }, [text(categoryLabel(String(r.Category)))])];
    if (String(r.Category) === "version-update") {
      const weak = String(r.Evidence) === "weak";
      const ident = r.IdentityKey ? ` · ${r.IdentityKey}` : "";
      badges.push(
        el("span", { class: `badge ${weak ? "badge-weak" : "badge-strong"}` }, [
          text(weak ? `元数据弱键「疑似」${ident}` : `OnlineID 强键${ident}`),
        ]),
      );
    }
    if (String(r.Category) === "restore") {
      badges.push(el("span", { class: "badge badge-deleted-at" }, [text(`删除于 ${fmtMillis(r.DeletedAt)}`)]));
    }
    if (r.SkipReason) {
      badges.push(el("span", { class: "badge badge-skipreason" }, [text(`将被跳过：${r.SkipReason}`)]));
    }

    const rowMain = el("div", { class: "row-main" }, [
      cb,
      el("div", { class: "row-body" }, [
        el("div", { class: "row-title" }, [
          el("span", { class: "row-summary" }, [text(r.Summary)]),
          ...badges,
          ...(EXPORTABLE_KINDS.has(String(r.Kind)) ? [renderExportBtn(r)] : []),
        ]),
        el("div", { class: "row-key muted" }, [text(`${kindLabel(String(r.Kind))} · ${r.ContentKey}`)]),
      ]),
    ]);

    const row = el("div", { class: "diff-row" }, [rowMain]);
    if (r.ConflictFields && r.ConflictFields.length > 0) {
      const detail = el("div", { class: "conflict-detail hidden" });
      const table = el("table", { class: "conflict-table" });
      table.append(
        el("tr", {}, [el("th", {}, [text("字段")]), el("th", {}, [text("hub 真相")]), el("th", {}, [text("目标 fork")])]),
      );
      for (const f of r.ConflictFields) {
        table.append(el("tr", {}, [el("td", {}, [text(f.Field)]), el("td", {}, [text(f.HubValue)]), el("td", {}, [text(f.ForkValue)])]));
      }
      detail.append(table);
      const toggle = el("button", { class: "btn btn-ghost btn-sm" }, [text("字段级对比")]);
      toggle.addEventListener("click", () => {
        detail.classList.toggle("hidden");
      });
      row.append(el("div", { class: "row-extra" }, [toggle]), detail);
    }
    return row;
  }

  /** 统计条（§6.2）：全量口径（Tab/筛选只是查看切片，计划由勾选决定）——
   * 将同步 N 条 / 跳过 M 条·k 类原因（SkipReason 分组，未标注原因的归
   * 「用户未勾选」）。 */
  function renderStats(): void {
    clear(statsBar);
    let willSync = 0;
    const skipReasons = new Map<string, number>();
    for (const r of rows) {
      if (checked.get(rowKey(r)) === true) {
        willSync++;
        continue;
      }
      const reason = r.SkipReason || "用户未勾选";
      skipReasons.set(reason, (skipReasons.get(reason) ?? 0) + 1);
    }
    const skipped = [...skipReasons.values()].reduce((a, b) => a + b, 0);
    const reasonParts = [...skipReasons.entries()].map(([reason, n]) => `${reason}×${n}`);
    statsBar.append(
      el("span", { class: "stat-main" }, [
        text(`将同步 ${willSync} 条 / 跳过 ${skipped} 条 · ${skipReasons.size} 类原因`),
      ]),
      el("span", { class: "muted" }, [text(reasonParts.length > 0 ? reasonParts.join(" · ") : "无差异数据")]),
    );
  }

  renderTabs();
  renderFilters();
  renderList();
}
