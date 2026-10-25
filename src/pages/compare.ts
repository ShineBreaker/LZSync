// 对比预览页（§6.2）：模式选择（hub→fork 投放预演 / fork↔fork 快照对比）、
// 类型 Tab（谱面集/难度/收藏/皮肤/成绩/键位/规则集设置/ModPreset）与
// 分类筛选、「需用户确认」分组
//（元数据弱键，默认不勾选）、差异列表（分类徽标含「恢复（已删除）」与
// 强/弱键标注、行级勾选、冲突字段展开）、底部统计条（N 条同步 / M 条
// 跳过·k 类原因）、生成投放计划。

import { t, trBackend, type DictKey } from "../i18n";
import { CompareService, ExportService, LibraryService } from "../mygo";
import { categoryClass, categoryLabel, clear, el, errText, fmtMillis, kindLabel, message, text } from "../ui";

/** 类型 Tab（§6.2 数据面八类 + 全部；「全部」默认不含成绩）。 */
const KIND_TABS: { id: string; labelKey: DictKey }[] = [
  { id: "all", labelKey: "kind.all" },
  { id: "beatmap_set", labelKey: "kind.beatmap_set" },
  { id: "beatmap", labelKey: "kind.beatmap" },
  { id: "collection", labelKey: "kind.collection" },
  { id: "skin", labelKey: "kind.skin" },
  { id: "score", labelKey: "kind.score" },
  { id: "key_binding", labelKey: "kind.key_binding" },
  { id: "ruleset_setting", labelKey: "kind.ruleset_setting" },
  { id: "mod_preset", labelKey: "kind.mod_preset" },
];

/** 分类筛选（纯结果筛选，§6.2）。 */
const CAT_FILTERS: { id: string; labelKey: DictKey }[] = [
  { id: "all", labelKey: "cmp.filterAll" },
  { id: "create", labelKey: "cmp.filterCreate" },
  { id: "conflict", labelKey: "cmp.filterConflict" },
  { id: "version-update", labelKey: "cmp.filterVersion" },
  { id: "restore", labelKey: "cmp.filterRestore" },
  { id: "target-only", labelKey: "cmp.filterTargetOnly" },
];

/** 有容器形态的类型（§7-M3-5 路线④：谱面集 .osz / 皮肤 .osk / 成绩 .osr）。 */
const EXPORTABLE_KINDS = new Set(["beatmap_set", "skin", "score"]);

type DiffRowView = Awaited<ReturnType<typeof CompareService.previewDeploy>>[number];

export async function renderCompare(root: HTMLElement): Promise<void> {
  clear(root);
  root.append(el("h1", { class: "page-title" }, [text(t("nav.compare"))]));

  // 页面状态。
  let mode: "preview" | "forks" = "preview";
  let kindFilter = "all";
  let catFilter = "all";
  let rows: DiffRowView[] = [];
  const checked = new Map<string, boolean>();

  // 控件。
  const modeSel = el("select", { class: "input input-s" }, [
    el("option", { value: "preview" }, [text(t("cmp.modePreview"))]),
    el("option", { value: "forks" }, [text(t("cmp.modeForks"))]),
  ]);
  const sourceSel = el("select", { class: "input input-s" });
  const targetSel = el("select", { class: "input input-s" });
  const queryBtn = el("button", { class: "btn btn-primary" }, [text(t("cmp.query"))]);
  const controls = el("div", { class: "toolbar" }, [modeSel, sourceSel, targetSel, queryBtn]);
  const statusLine = el("div", { class: "status-line" });
  const tabsBar = el("div", { class: "tabs" });
  const filtersBar = el("div", { class: "filters" });
  const listBox = el("div", { class: "diff-list" });
  const statsBar = el("div", { class: "stats-bar" });
  const buildBtn = el("button", { class: "btn btn-primary" }, [text(t("cmp.buildPlan"))]);
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
    statusLine.append(message("info", t("cmp.querying")));
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
      statusLine.append(message("info", t("cmp.noSelection")));
      return;
    }
    buildBtn.disabled = true;
    clear(statusLine);
    try {
      const plan = await CompareService.buildPlan(targetSel.value, sel);
      const actions = plan.Items.filter((i) => i.Action !== "skip").length;
      const skips = plan.Items.length - actions;
      statusLine.append(
        message("ok", t("cmp.planBuilt", plan.TargetFork, actions, skips)),
      );
    } catch (err) {
      statusLine.append(message("error", errText(err)));
    } finally {
      buildBtn.disabled = false;
    }
  });

  /** 行级「导出容器」入口（§7-M3-5 路线④降级）。 */
  function renderExportBtn(r: DiffRowView): HTMLElement {
    const btn = el("button", {
      class: "btn btn-ghost btn-sm",
      title: t("cmp.exportTitle"),
    }, [text(t("cmp.exportBtn"))]);
    btn.addEventListener("click", async () => {
      btn.disabled = true;
      try {
        const res = await ExportService.exportContainers(targetSel.value, String(r.Kind), [r.ContentKey]);
        clear(statusLine);
        const where = t("cmp.exported", res.Paths.length, res.Paths.join(", "));
        const note = res.Note ? ` (${trBackend(res.Note)})` : "";
        statusLine.append(message(res.Note ? "info" : "ok", `${where}${note}`));
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
      const btn = el("button", { class: `tab ${kindFilter === tab.id ? "tab-active" : ""}` }, [text(t(tab.labelKey))]);
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
      const btn = el("button", { class: `chip ${catFilter === f.id ? "chip-active" : ""}` }, [text(t(f.labelKey))]);
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
      listBox.append(el("p", { class: "muted" }, [text(t("cmp.emptyHint"))]));
      renderStats();
      return;
    }
    if (kindFilter === "beatmap") {
      listBox.append(
        el("p", { class: "muted" }, [text(t("cmp.beatmapHint"))]),
      );
      renderStats();
      return;
    }
    const visible = visibleRows();
    if (visible.length === 0) {
      listBox.append(el("p", { class: "muted" }, [text(t("cmp.noMatch"))]));
      renderStats();
      return;
    }
    const needsConfirm = visible.filter((r) => String(r.Evidence) === "weak");
    const normal = visible.filter((r) => String(r.Evidence) !== "weak");
    if (needsConfirm.length > 0) {
      listBox.append(
        el("div", { class: "group-title" }, [text(t("cmp.needsConfirmTitle"))]),
        el("p", { class: "muted" }, [text(t("cmp.needsConfirmDesc"))]),
      );
      for (const r of needsConfirm) {
        listBox.append(renderRow(r));
      }
      if (normal.length > 0) {
        listBox.append(el("div", { class: "group-title" }, [text(t("cmp.otherDiffs"))]));
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
          text(weak ? t("cmp.weakBadge", ident) : t("cmp.strongBadge", ident)),
        ]),
      );
    }
    if (String(r.Category) === "restore") {
      badges.push(el("span", { class: "badge badge-deleted-at" }, [text(t("cmp.deletedAt", fmtMillis(r.DeletedAt)))]));
    }
    if (r.SkipReason) {
      badges.push(el("span", { class: "badge badge-skipreason" }, [text(t("cmp.skipBadge", trBackend(r.SkipReason)))]));
    }

    const rowMain = el("div", { class: "row-main" }, [
      cb,
      el("div", { class: "row-body" }, [
        el("div", { class: "row-title" }, [
          el("span", { class: "row-summary" }, [text(trBackend(r.Summary))]),
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
        el("tr", {}, [el("th", {}, [text(t("cmp.field"))]), el("th", {}, [text(t("cmp.hubValue"))]), el("th", {}, [text(t("cmp.forkValue"))])]),
      );
      for (const f of r.ConflictFields) {
        table.append(el("tr", {}, [el("td", {}, [text(f.Field)]), el("td", {}, [text(f.HubValue)]), el("td", {}, [text(f.ForkValue)])]));
      }
      detail.append(table);
      const toggle = el("button", { class: "btn btn-ghost btn-sm" }, [text(t("cmp.diffFields"))]);
      toggle.addEventListener("click", () => {
        detail.classList.toggle("hidden");
      });
      row.append(el("div", { class: "row-extra" }, [toggle]), detail);
    }
    return row;
  }

  function renderStats(): void {
    clear(statsBar);
    let willSync = 0;
    const skipReasons = new Map<string, number>();
    for (const r of rows) {
      if (checked.get(rowKey(r)) === true) {
        willSync++;
        continue;
      }
      const reason = r.SkipReason ? trBackend(r.SkipReason) : t("cmp.userUnchecked");
      skipReasons.set(reason, (skipReasons.get(reason) ?? 0) + 1);
    }
    const skipped = [...skipReasons.values()].reduce((a, b) => a + b, 0);
    const reasonParts = [...skipReasons.entries()].map(([reason, n]) => `${reason} ×${n}`);
    statsBar.append(
      el("span", { class: "stat-main" }, [
        text(t("cmp.statsMain", willSync, skipped)),
      ]),
      el("span", { class: "muted" }, [text(reasonParts.length > 0 ? reasonParts.join(" · ") : t("cmp.noDiffData"))]),
    );
  }

  renderTabs();
  renderFilters();
  renderList();
}
