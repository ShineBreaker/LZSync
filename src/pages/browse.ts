// 数据浏览页：侧栏数据域对应主内容页（DESIGN.md §数据浏览）。
// 行结构 = 封面 + 标题/作者 + 状态点 + 统计 + x/y 同步块；悬浮层列各端明细。
// 当前实现：beatmap_set / collection / skin / score 四类，kind 由调用方传入。

import { t, trBackend } from "../i18n";
import { CompareService, ExportService, LibraryService } from "../mygo";
import { clear, el, errText, fmtBytes, fmtMillis, message, popoverAt, popoverForkRow, syncStateClass, syncStateLabel, text, type SyncState } from "../ui";

type DiffRow = Awaited<ReturnType<typeof CompareService.previewDeploy>>[number];
type LibCard = Awaited<ReturnType<typeof LibraryService.list>>[number];

interface RowView {
  key: string;
  kind: string;
  title: string;
  sub: string;
  meta: string;
  state: SyncState;
  evidence: string;
  deletedAt: number;
  conflictFields?: { Field: string; HubValue: string; ForkValue: string }[];
  skipReason: string;
  contentKey: string;
  exportable: boolean;
}

const EXPORTABLE = new Set(["beatmap_set", "skin", "score"]);

export async function renderBrowse(root: HTMLElement, kind: string): Promise<void> {
  clear(root);
  root.append(el("h1", { class: "page-title" }, [text(t(`nav.${kind === "beatmap_set" ? "beatmaps" : kind === "collection" ? "collections" : kind === "skin" ? "skins" : "scores"}`))]));

  const statusLine = el("div", { class: "status-line" });
  const toolbar = el("div", { class: "toolbar" });
  const listBox = el("div");
  root.append(toolbar, statusLine, listBox);

  let targetFork = "";
  const checked = new Set<string>();
  let rows: RowView[] = [];
  let allCards: LibCard[] = [];

  // 筛选：全部 / 仅差异 / 冲突 / 需确认
  let filter: "all" | "diff" | "conflict" | "needsConfirm" = "all";

  try {
    allCards = await LibraryService.list();
  } catch {
    allCards = [];
  }

  // 目标选择器（sync 的目标端）
  const targetSel = el("select", { class: "input" }) as HTMLSelectElement;
  for (const c of allCards) {
    targetSel.append(el("option", { value: c.ForkID }, [text(c.ForkID)]));
  }
  if (allCards.length > 0) {
    const first = allCards[0];
    if (first) targetFork = first.ForkID;
  }
  targetSel.addEventListener("change", () => {
    targetFork = targetSel.value;
    void load();
  });

  // 筛选按钮
  const filterBtns = new Map<string, HTMLButtonElement>();
  for (const f of ["all", "diff", "conflict", "needsConfirm"] as const) {
    const label = f === "all" ? t("browse.filterAll") : f === "diff" ? t("browse.filterDiff") : f === "conflict" ? t("browse.filterConflict") : t("browse.filterNeedsConfirm");
    const btn = el("button", { class: `btn${f === filter ? " primary" : ""}` }, [text(label)]);
    btn.addEventListener("click", () => {
      filter = f;
      for (const [k, b] of filterBtns) {
        b.classList.toggle("primary", k === f);
      }
      renderList();
    });
    filterBtns.set(f, btn);
  }

  const syncBtn = el("button", { class: "btn primary" }, [text(t("browse.syncBtn"))]);
  syncBtn.addEventListener("click", async () => {
    if (checked.size === 0 || !targetFork) {
      clear(statusLine);
      statusLine.append(message("info", t("sync.noPlan")));
      return;
    }
    syncBtn.disabled = true;
    clear(statusLine);
    try {
      const sel = rows.filter((r) => checked.has(r.key)).map((r) => ({ Kind: r.kind, ContentKey: r.contentKey }));
      await CompareService.buildPlan(targetFork, sel);
      statusLine.append(message("ok", `${t("sync.pushDesc")} ${targetFork}`));
    } catch (err) {
      statusLine.append(message("error", errText(err)));
    } finally {
      syncBtn.disabled = false;
    }
  });

  toolbar.append(
    el("span", { class: "page-meta", id: "browse-meta" }, [text("…")]),
    el("span", { class: "spacer" }),
    ...[...filterBtns.values()],
    targetSel,
    syncBtn,
  );

  // presence: kind\u0000content_key → Set<forkID>
  let presenceByKey = new Map<string, Set<string>>();

  async function load(): Promise<void> {
    if (!targetFork) return;
    clear(statusLine);
    statusLine.append(message("info", t("browse.loading")));
    try {
      const [preview, deleted, presence] = await Promise.all([
        CompareService.previewDeploy(targetFork),
        CompareService.deletedEntries(targetFork),
        CompareService.forkPresence(kind as never),
      ]);
      presenceByKey = new Map();
      for (const p of presence) {
        const k = `${p.Kind}\u0000${p.ContentKey}`;
        if (!presenceByKey.has(k)) presenceByKey.set(k, new Set());
        presenceByKey.get(k)!.add(p.ForkID);
      }
      const seen = new Set(preview.map((r) => `${r.Kind}\u0000${r.ContentKey}`));
      const merged = [...preview];
      for (const d of deleted) {
        const k = `${d.Kind}\u0000${d.ContentKey}`;
        if (!seen.has(k)) merged.push(d);
      }
      rows = merged.map(toRowView).filter((r) => r.kind === kind);
      checked.clear();
      for (const r of rows) {
        if (r.evidence !== "weak" && r.state !== "synced") checked.add(r.key);
      }
      clear(statusLine);
      const metaEl = toolbar.querySelector("#browse-meta");
      if (metaEl) metaEl.textContent = t("browse.total", rows.length);
      renderList();
    } catch (err) {
      clear(statusLine);
      statusLine.append(message("error", errText(err)));
    }
  }

  function toRowView(r: DiffRow): RowView {
    const key = `${r.Kind}\u0000${r.ContentKey}`;
    const cat = String(r.Category);
    const evidence = String(r.Evidence);
    let state: SyncState = "synced";
    if (cat === "conflict") state = "conflict";
    else if (cat === "version-update" && evidence === "weak") state = "needsConfirm";
    else if (cat === "version-update") state = "update";
    else if (cat === "target-only") state = "targetOnly";
    else if (cat === "restore") state = "synced"; // 恢复组另算
    else if (cat === "create") state = "update";

    const summary = trBackend(r.Summary);
    const { title, sub } = splitSummary(summary);
    const meta = buildMeta(r);

    return {
      key,
      kind: String(r.Kind),
      title,
      sub,
      meta,
      state,
      evidence,
      deletedAt: r.DeletedAt,
      conflictFields: r.ConflictFields ?? undefined,
      skipReason: r.SkipReason,
      contentKey: r.ContentKey,
      exportable: EXPORTABLE.has(String(r.Kind)),
    };
  }

  function splitSummary(summary: string): { title: string; sub: string } {
    // "Artist - Title" 或 "[Owner] Name" 或裸标题
    const dashIdx = summary.indexOf(" - ");
    if (dashIdx > 0) {
      return { title: summary.slice(dashIdx + 3), sub: summary.slice(0, dashIdx) };
    }
    return { title: summary, sub: "" };
  }

  function buildMeta(r: DiffRow): string {
    const parts: string[] = [];
    if (r.Kind === "beatmap_set") {
      parts.push(t("browse.hubVersion", r.IdentityKey ? r.IdentityKey.replace(/^(online|meta):/, "") : "—"));
    }
    if (r.SkipReason) {
      parts.push(trBackend(r.SkipReason));
    }
    return parts.join(" · ");
  }

  function rowHeight(r: RowView): string {
    if (r.kind === "beatmap_set") return "data-row has-cover";
    return "data-row compact";
  }

  function renderList(): void {
    clear(listBox);
    if (rows.length === 0) {
      listBox.append(el("p", { class: "page-meta" }, [text(t("browse.empty"))]));
      return;
    }
    let filtered = rows;
    if (filter === "diff") filtered = rows.filter((r) => r.state !== "synced");
    if (filter === "conflict") filtered = rows.filter((r) => r.state === "conflict");
    if (filter === "needsConfirm") filtered = rows.filter((r) => r.state === "needsConfirm");

    for (const r of filtered) {
      listBox.append(renderRow(r));
    }
  }

  function renderRow(r: RowView): HTMLElement {
    const rowEl = el("div", { class: rowHeight(r) });

    // 勾选框
    const cb = el("input", { type: "checkbox", class: "row-check" }) as HTMLInputElement;
    cb.checked = checked.has(r.key);
    cb.addEventListener("change", () => {
      if (cb.checked) checked.add(r.key);
      else checked.delete(r.key);
      rowEl.classList.toggle("checked", cb.checked);
      updateToolbar();
    });

    // 主信息
    const main = el("div", { class: "row-main" }, [
      el("div", { class: "row-title" }, [text(r.title)]),
      ...(r.sub ? [el("div", { class: "row-sub" }, [text(r.sub)])] : []),
      el("div", { class: "row-meta" }, [
        ...(r.meta ? [el("span", {}, [text(r.meta)])] : []),
        ...(r.deletedAt > 0 ? [el("span", {}, [text(fmtMillis(r.deletedAt))])] : []),
      ]),
    ]);

    // 右侧同步块（x/y = 持有该 content_key 的端数 / 已注册端数）
    const sync = el("div", { class: `sync-block ${syncStateClass(r.state)}` });
    const holders = presenceByKey.get(r.key) ?? new Set<string>();
    const totalForks = allCards.length;
    const xylabel = `${holders.size}/${totalForks}`;
    sync.append(
      el("span", { class: "n" }, [text(xylabel)]),
      el("span", { class: "s" }, [text(syncStateLabel(r.state))]),
    );

    // 封面（仅谱面集）：content_key → data URL，设置到行 background。
    if (r.kind === "beatmap_set") {
      CompareService.coverImage(r.contentKey).then((url) => {
        if (url) {
          rowEl.style.backgroundImage = `url("${url}")`;
          rowEl.classList.add("has-cover");
        }
      }).catch(() => {});
    }

    // 悬浮层：各端持有状态（presence 命中=该端持有；state=hub 视角的行态）
    popoverAt(sync, (pop) => {
      for (const c of allCards) {
        const has = holders.has(c.ForkID);
        const st = !has ? "missing" : r.state === "conflict" ? "err" : r.state === "needsConfirm" ? "warn" : "ok";
        const label = !has ? t("st.missing") : st === "ok" ? t("st.synced") : st === "err" ? t("st.conflict") : st === "warn" ? t("st.needsConfirm") : t("st.update");
        pop.append(popoverForkRow(c.ForkID, st, label));
      }
      if (r.meta) {
        pop.append(el("div", { class: "popover-foot" }, [text(r.meta)]));
      }
    });

    // 冲突行：可点击展开差异表
    if (r.conflictFields && r.conflictFields.length > 0) {
      const detail = el("div", { class: "popover hidden" });
      const table = el("table", {});
      for (const f of r.conflictFields) {
        table.append(
          el("tr", {}, [
            el("td", {}, [text(f.Field)]),
            el("td", {}, [text(f.HubValue)]),
            el("td", {}, [text(f.ForkValue)]),
          ]),
        );
      }
      detail.append(table);
      rowEl.append(detail);
      rowEl.style.cursor = "pointer";
      rowEl.addEventListener("click", (e) => {
        if ((e.target as HTMLElement).closest(".sync-block, input[type=checkbox]")) return;
        detail.classList.toggle("hidden");
      });
    }

    rowEl.append(cb, main, sync);
    return rowEl;
  }

  function updateToolbar(): void {
    const metaEl = toolbar.querySelector("#browse-meta");
    if (metaEl) metaEl.textContent = `${t("browse.total", rows.length)} · ${t("browse.selected", checked.size)}`;
  }

  await load();
}
