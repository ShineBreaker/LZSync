// LZSync 应用壳：侧栏=数据域导航（DESIGN.md §侧栏），顶部=搜索+全局警告。
// 同步进度事件统一订阅后分发给同步页。

import { getLang, onLangChange, t, type DictKey } from "./i18n";
import { LibraryService, events } from "./mygo";
import { renderBrowse, setBrowseFilter } from "./pages/browse";
import { renderConflicts } from "./pages/conflicts";
import { renderClients } from "./pages/clients";
import { dispatchProgress, detachProgress, renderSync, requestRun, setPendingPreview } from "./pages/sync";
import { renderSettings } from "./pages/settings";
import { clear, el, errText, setNavigator, text } from "./ui";

type PageId = "beatmaps" | "collections" | "skins" | "scores" | "conflicts" | "sync" | "clients" | "settings";

const PAGES: { id: PageId; labelKey: DictKey; render: (root: HTMLElement) => Promise<void> }[] = [
  { id: "beatmaps", labelKey: "nav.beatmaps", render: (r) => renderBrowse(r, "beatmap_set") },
  { id: "collections", labelKey: "nav.collections", render: (r) => renderBrowse(r, "collection") },
  { id: "skins", labelKey: "nav.skins", render: (r) => renderBrowse(r, "skin") },
  { id: "scores", labelKey: "nav.scores", render: (r) => renderBrowse(r, "score") },
  { id: "conflicts", labelKey: "nav.conflicts", render: renderConflicts },
  { id: "sync", labelKey: "nav.sync", render: renderSync },
  { id: "clients", labelKey: "nav.clients", render: renderClients },
  { id: "settings", labelKey: "nav.settings", render: renderSettings },
];

// 侧栏分组：数据域在前，操作页在后
const NAV_GROUPS: { sep?: DictKey; items: PageId[] }[] = [
  { items: ["beatmaps"] },
  { sep: "nav.sectionData", items: ["collections", "skins", "scores"] },
  { items: ["conflicts", "sync"] },
  { items: ["clients", "settings"] },
];

let current: PageId = "beatmaps";

const $ = <T extends HTMLElement>(selector: string): T => document.querySelector<T>(selector)!;

function renderNav(): void {
  const nav = $("#nav");
  clear(nav);
  for (const group of NAV_GROUPS) {
    if (group.sep) {
      nav.append(el("div", { class: "nav-sep" }, [text(t(group.sep))]));
    }
    for (const id of group.items) {
      const page = PAGES.find((p) => p.id === id);
      if (!page) continue;
      const btn = el("a", { class: `nav-item${id === current ? " active" : ""}` }, [text(t(page.labelKey))]);
      btn.dataset.page = id;
      btn.addEventListener("click", () => {
        void switchPage(id);
      });
      nav.append(btn);
    }
  }
}

async function main(): Promise<void> {
  document.documentElement.lang = getLang() === "en" ? "en" : "zh-CN";
  renderNav();

  onLangChange(() => {
    renderNav();
    void refreshSidebar();
    void switchPage(current);
  });

  // 进度事件统一订阅：分发给同步页。
  events.syncProgress.on((ev) => {
    if (current === "sync") {
      dispatchProgress(ev);
    }
  });

  // 跨页导航钩子：同步页待处理计数 → 数据页带筛选；数据页勾选 → 同步页。
  const PAGE_KIND: Partial<Record<PageId, string>> = {
    beatmaps: "beatmap_set",
    collections: "collection",
    skins: "skin",
    scores: "score",
  };
  setNavigator((page, filter) => {
    const kind = PAGE_KIND[page as PageId];
    if (kind && filter) {
      setBrowseFilter(kind, filter as "conflict" | "needsConfirm");
    }
    void switchPage(page as PageId);
  });

  // 顶栏常驻「同步」按钮：切到同步页并跑一轮（清掉数据页勾选的预览）。
  const topSync = $("#topbar-sync");
  topSync.textContent = t("nav.sync");
  topSync.addEventListener("click", () => {
    setPendingPreview(null); // 顶栏 = 完整一轮，丢弃数据页勾选预览
    requestRun();
    void switchPage("sync");
  });

  await refreshSidebar();
  await switchPage("beatmaps");
}

async function switchPage(id: PageId): Promise<void> {
  current = id;
  detachProgress();
  for (const btn of Array.from(document.querySelectorAll<HTMLElement>(".nav-item"))) {
    btn.classList.toggle("active", btn.dataset.page === id);
  }
  const page = PAGES.find((p) => p.id === id);
  const content = $("#content");
  clear(content);
  if (page) {
    try {
      await page.render(content);
    } catch (err) {
      content.append(el("p", { class: "message err" }, [text(errText(err))]));
    }
  }
  // 客户端页与同步页都依赖最新侧栏状态
  if (id === "clients" || id === "sync") {
    await refreshSidebar();
  }
}

/** 侧栏客户端卡 + 顶部警告胶囊（DESIGN.md §侧栏/顶栏）。 */
async function refreshSidebar(): Promise<void> {
  const statusEl = $("#side-client-status");
  const warnEl = $("#side-client-warn");
  const cardEl = $("#side-client-card");
  const globalWarn = $("#global-warning");
  clear(statusEl);
  warnEl.classList.add("hidden");
  globalWarn.classList.add("hidden");
  cardEl.classList.remove("hidden");
  try {
    const cards = await LibraryService.list();
    const total = cards.length;
    const running = cards.filter((c) => c.ProcessRunning).length;
    if (total === 0) {
      statusEl.textContent = t("sidebar.noClients");
      return;
    }
    statusEl.textContent = t("sidebar.clientsSummary", total, running === 0 ? t("sidebar.noClients") : t("sidebar.running", running));
    if (running > 0) {
      const runningName = cards.find((c) => c.ProcessRunning)?.ForkID ?? "";
      warnEl.textContent = t("sidebar.running", runningName);
      warnEl.classList.remove("hidden");
      globalWarn.textContent = t("global.gameRunning", runningName);
      globalWarn.classList.remove("hidden");
    }
  } catch {
    statusEl.textContent = t("sidebar.statusUnavailable");
  }
}

void main();
