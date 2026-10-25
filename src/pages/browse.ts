// 数据浏览页：侧栏数据域对应主内容页（DESIGN.md §数据浏览）。
// 行结构 = 封面 + 标题/作者 + 状态点 + 统计 + x/y 同步块；悬浮层列各端明细。
// 当前实现：beatmap_set / collection / skin / score 四类，kind 由调用方传入。

import { t, trBackend } from "../i18n";
import { CompareService, ExportService, LibraryService, SyncService } from "../mygo";
import { setPendingPreview } from "./sync";
import { clear, el, errText, message, navigate, popoverAt, popoverForkRow, syncStateClass, syncStateLabel, text, type SyncState } from "../ui";

type DiffRow = Awaited<ReturnType<typeof CompareService.previewDeploy>>[number];
type LibCard = Awaited<ReturnType<typeof LibraryService.list>>[number];

type BrowseFilter = "all" | "diff" | "conflict" | "needsConfirm";

// 同步页待处理计数点进来的目标筛选（kind → filter，一次性消费）。
const pendingFilters = new Map<string, BrowseFilter>();

/** setBrowseFilter 供同步页跳转到指定数据页时带筛选。 */
export function setBrowseFilter(kind: string, filter: BrowseFilter): void {
  pendingFilters.set(kind, filter);
}

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

  const checked = new Set<string>();
  let rows: RowView[] = [];
  let allCards: LibCard[] = [];

  // 筛选：全部 / 仅差异 / 冲突 / 需确认（同步页待处理计数可带初始筛选进来）
  let filter: BrowseFilter = pendingFilters.get(kind) ?? "all";
  pendingFilters.delete(kind);

  try {
    allCards = await LibraryService.list();
  } catch {
    allCards = [];
  }

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
    if (checked.size === 0 || allCards.length === 0) {
      clear(statusLine);
      statusLine.append(message("info", t("sync.noPlan")));
      return;
    }
    syncBtn.disabled = true;
    clear(statusLine);
    try {
      const sel = rows.filter((r) => checked.has(r.key)).map((r) => ({ Kind: r.kind, ContentKey: r.contentKey }));
      // 一轮编排：勾选经 prepareSelected 预演，预览交同步页确认执行。
      const preview = await SyncService.prepareSelected(sel);
      setPendingPreview(preview);
      navigate("sync");
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
    syncBtn,
  );

  // presence: kind\u0000content_key → Set<forkID>
  let presenceByKey = new Map<string, Set<string>>();

  // 状态严重度：取各 fork 间的最差态做整行状态（同一条目可能在 delta 是「新增」在 g0v0 是「冲突」）。
  const STATE_RANK: Record<SyncState, number> = { synced: 0, targetOnly: 1, update: 2, needsConfirm: 3, conflict: 4 };

  async function load(): Promise<void> {
    if (allCards.length === 0) return;
    clear(statusLine);
    statusLine.append(message("info", t("browse.loading")));
    try {
      const forks = allCards.map((c) => c.ForkID);
      // 并发拉所有 fork 的 preview/deleted/presence，再做 union 聚合。
      const [previews, deleteds, presence] = await Promise.all([
        Promise.all(forks.map((f) => CompareService.previewDeploy(f))),
        Promise.all(forks.map((f) => CompareService.deletedEntries(f))),
        CompareService.forkPresence(kind as never),
      ]);
      presenceByKey = new Map();
      for (const p of presence) {
        const k = `${p.Kind}\u0000${p.ContentKey}`;
        if (!presenceByKey.has(k)) presenceByKey.set(k, new Set());
        presenceByKey.get(k)!.add(p.ForkID);
      }
      // union：key → 最严重态的 DiffRow（同一 key 多端分类不同时，取 rank 最高的一条）
      const byKey = new Map<string, DiffRow>();
      const consider = (r: DiffRow): void => {
        const k = `${r.Kind}\u0000${r.ContentKey}`;
        const prev = byKey.get(k);
        if (!prev) { byKey.set(k, r); return; }
        const a = toRowView(prev).state;
        const b = toRowView(r).state;
        if (STATE_RANK[b] > STATE_RANK[a]) byKey.set(k, r);
      };
      for (const pv of previews) for (const r of pv) consider(r);
      for (const dl of deleteds) for (const r of dl) consider(r);
      rows = [...byKey.values()].map(toRowView).filter((r) => r.kind === kind);
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
    else if (cat === "restore") state = "targetOnly"; // 恢复组：目标已删但 hub 有副本（DeletedAt 在 meta 显示）
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
    // beatmap_set：meta 仅当有真实可读的 OnlineID（不是 -1 这种「无外部 ID」哨兵）
    if (r.Kind === "beatmap_set" && r.IdentityKey) {
      const idv = r.IdentityKey.replace(/^online:/, "");
      if (r.IdentityKey.startsWith("online:") && idv !== "-1" && idv !== "0") {
        parts.push(`#${idv}`);
      }
    }
    if (r.DeletedAt) {
      const d = new Date(r.DeletedAt);
      const stamp = `${d.getFullYear()}/${String(d.getMonth()+1).padStart(2,"0")}/${String(d.getDate()).padStart(2,"0")}`;
      parts.push(t("browse.deletedOn", stamp));
    }
    if (r.SkipReason) {
      parts.push(trBackend(r.SkipReason));
    }
    return parts.join(" · ");
  }

  function rowHeight(r: RowView): string {
    // has-cover 只在真的有封面 URL 时才加（异步赋值后补），初始一律无 has-cover
    let cls = r.kind === "beatmap_set" ? "data-row" : "data-row compact";
    if (r.state === "conflict") cls += " err-row";
    else if (r.state === "needsConfirm") cls += " warn-row";
    return cls;
  }

  // 懒渲染：首屏只渲 PAGE_SIZE 行，滚动到底再追加。coverImage 只对
  // 实际渲染出的行发 IPC，避免 3087 行 × IPC 打爆。
  const PAGE_SIZE = 80;
  let shown = 0;
  let filteredCache: RowView[] = [];
  let sentinel: HTMLElement | null = null;
  const coverCache = new Map<string, Promise<string>>(); // contentKey → url promise

  function getCover(key: string): Promise<string> {
    if (!coverCache.has(key)) {
      coverCache.set(key, CompareService.coverImage(key).catch(() => ""));
    }
    return coverCache.get(key)!;
  }

  function renderList(): void {
    clear(listBox);
    shown = 0;
    if (rows.length === 0) {
      listBox.append(el("p", { class: "page-meta" }, [text(t("browse.empty"))]));
      return;
    }
    filteredCache = rows;
    if (filter === "diff") filteredCache = rows.filter((r) => r.state !== "synced");
    if (filter === "conflict") filteredCache = rows.filter((r) => r.state === "conflict");
    if (filter === "needsConfirm") filteredCache = rows.filter((r) => r.state === "needsConfirm");
    appendMore();
  }

  function appendMore(): void {
    if (sentinel) {
      sentinel.remove();
      sentinel = null;
    }
    const slice = filteredCache.slice(shown, shown + PAGE_SIZE);
    for (const r of slice) {
      listBox.append(renderRow(r));
    }
    shown += slice.length;
    if (shown < filteredCache.length) {
      sentinel = el("div", { class: "page-meta", style: "padding:20px;text-align:center" }, [text(`… ${filteredCache.length - shown} …`)]);
      listBox.append(sentinel);
      // IntersectionObserver 哨兵
      const io = new IntersectionObserver((entries) => {
        for (const e of entries) {
          if (e.isIntersecting) {
            io.disconnect();
            appendMore();
          }
        }
      }, { rootMargin: "400px" });
      io.observe(sentinel);
    }
  }

  function renderRow(r: RowView): HTMLElement {
    const rowEl = el("div", { class: rowHeight(r) });

    // 勾选框（cb.checked 变化必须同步 rowEl.checked class，否则 CSS ::after 不绘）
    const cb = el("input", { type: "checkbox", class: "row-check" }) as HTMLInputElement;
    cb.checked = checked.has(r.key);
    rowEl.classList.toggle("checked", cb.checked);
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

    // 封面（仅谱面集）：data URL，设置到行 background。走缓存避免重复 IPC。
    if (r.kind === "beatmap_set") {
      getCover(r.contentKey).then((url) => {
        if (url) {
          rowEl.style.backgroundImage = `url("${url}")`;
          rowEl.classList.add("has-cover");
        }
      });
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
      const detail = el("div", { class: "row-detail hidden" });
      const table = el("table", { class: "row-detail-table" });
      const head = el("tr", {}, [
        el("th", {}, [text("")]),
        el("th", {}, [text(t("browse.conflictHub"))]),
        el("th", {}, [text(t("browse.conflictFork"))]),
      ]);
      table.append(head);
      for (const f of r.conflictFields) {
        table.append(
          el("tr", {}, [
            el("td", { class: "k" }, [text(f.Field)]),
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
