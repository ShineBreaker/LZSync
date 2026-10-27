// 应用壳与路由（DESIGN.md §9）：侧栏 = 品牌 + 导航（同步徽标 / 四类计数）
// + 底部设置；内容区按页签渲染；底部居中暂存条；进度事件接 state.ts
// 的运行态机；onFileDrop 全页可投。

import { onFileDrop } from "mygo-runtime";
import { t } from "./i18n";
import { CompareService, events } from "./mygo";
import type { EntityKind } from "./mygo";
import {
  confirmRun,
  handleProgress,
  onData,
  onRun,
  runStaged,
  runView,
  staged,
} from "./state";
import { clear, el, icon, text } from "./ui";
import { renderHome, registerPaths } from "./pages/home";
import { renderLibrary } from "./pages/library";
import { renderSettings } from "./pages/settings";

type Route = "home" | EntityKind | "settings";

const NAV: { route: Route; label: () => string }[] = [
  { route: "home", label: () => t("nav.sync") },
  { route: "beatmap_set", label: () => t("nav.beatmap_set") },
  { route: "collection", label: () => t("nav.collection") },
  { route: "skin", label: () => t("nav.skin") },
  { route: "score", label: () => t("nav.score") },
];

let route: Route = "home";
let cleanup: (() => void) | null = null;
let inboxCount = 0;
let counts: Record<string, number> = {};

const content = document.getElementById("content")!;
const nav = document.getElementById("nav")!;
const sideBottom = document.getElementById("side-bottom")!;
const bar = document.getElementById("staged-bar")!;

// ---------- 侧栏 ----------

function paintNav(): void {
  clear(nav);
  for (const n of NAV) {
    const item = el("a", { class: `nav-item${route === n.route ? " active" : ""}` });
    item.append(el("span", { class: "nl" }, [text(n.label())]));
    if (n.route === "home") {
      const v = runView();
      if (v.phase === "preparing" || v.phase === "committing") {
        item.append(el("span", { class: "nav-progress" }));
      } else if (inboxCount > 0) {
        item.append(el("span", { class: "nav-badge" }, [text(String(inboxCount))]));
      }
    } else {
      const c = counts[n.route];
      if (c !== undefined) item.append(el("span", { class: "nav-count" }, [text(String(c))]));
    }
    item.addEventListener("click", () => navigate(n.route));
    nav.append(item);
  }
  clear(sideBottom);
  const st = el("a", { class: `nav-item${route === "settings" ? " active" : ""}` }, [
    el("span", { class: "nl" }, [text(t("nav.settings"))]),
  ]);
  st.addEventListener("click", () => navigate("settings"));
  sideBottom.append(st);
}

async function refreshCounts(): Promise<void> {
  try {
    const [cf, lib, hc] = await Promise.all([
      CompareService.listConflicts(),
      CompareService.library("beatmap_set" as EntityKind),
      CompareService.hubCounts(),
    ]);
    inboxCount = cf.length + lib.filter((i) => i.State === "confirm").length;
    counts = hc;
  } catch {
    // 计数失败只影响徽标——导航本身照常。
  }
  paintNav();
}

// ---------- 路由 ----------

export function navigate(r: Route): void {
  if (route === r) return;
  route = r;
  paintNav();
  paintPage();
}

function paintPage(): void {
  cleanup?.();
  cleanup = null;
  switch (route) {
    case "home":
      cleanup = renderHome(content);
      break;
    case "settings":
      cleanup = renderSettings(content);
      break;
    default:
      cleanup = renderLibrary(content, route);
  }
}

// ---------- 暂存条（§5：底部居中，二步确认与库卡同状态机） ----------

function paintBar(): void {
  clear(bar);
  const v = runView();
  const n = staged.size;
  if (v.doneFlash > 0) {
    bar.classList.remove("hidden");
    bar.append(el("span", { class: "done-flash" }, [icon("check"), text(`✓ ${v.doneFlash}`)]));
    return;
  }
  if (n === 0) {
    bar.classList.add("hidden");
    return;
  }
  bar.classList.remove("hidden");
  bar.append(el("span", { class: "count" }, [text(t("bar.staged", n))]));
  const go = el("button", { class: "btn primary" });
  switch (v.phase) {
    case "ready":
      if (v.source === "selection" && v.total > 0) {
        go.textContent = t("btn.writeN", v.total);
        go.addEventListener("click", () => void confirmRun());
      } else if (v.source === "selection") {
        go.textContent = t("btn.uptodate");
        go.className = "btn ok";
        go.disabled = true;
      } else {
        go.textContent = t("btn.sync");
        go.disabled = true; // 全量轮确认在库卡上。
      }
      break;
    case "preparing":
    case "committing":
      go.textContent = t("btn.writing");
      go.disabled = true;
      break;
    default:
      go.textContent = t("btn.sync");
      go.addEventListener("click", () => void runStaged());
  }
  const close = el("button", { class: "btn icon" }, [icon("x")]);
  close.addEventListener("click", () => staged.clear());
  bar.append(go, close);
}

// ---------- 装配 ----------

events.syncProgress.on((ev) => {
  handleProgress(ev);
});

onRun(() => {
  paintBar();
  paintNav();
});
staged.onChange(paintBar);
onData(() => void refreshCounts());

onFileDrop(({ paths }) => {
  if (paths.length > 0) void registerPaths(paths);
});

window.addEventListener("lzsync:lang", () => {
  paintNav();
  paintBar();
  paintPage();
});

paintNav();
paintBar();
paintPage();
void refreshCounts();
