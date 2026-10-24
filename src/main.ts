// LZSync 应用壳（§6 四页导航）：左侧栏常驻 fork 状态灯与「游戏运行中」
// 全局警示条，主区按导航切换四页；syncProgress 事件统一订阅后分发给
// 同步执行页（M-2）。

import { LibraryService, events } from "./mygo";
import { renderCompare } from "./pages/compare";
import { renderLibraries } from "./pages/libraries";
import { dispatchProgress, detachProgress, renderRun } from "./pages/run";
import { renderSettings } from "./pages/settings";
import { clear, el, text } from "./ui";

type PageId = "libraries" | "compare" | "run" | "settings";

const PAGES: { id: PageId; label: string; render: (root: HTMLElement) => Promise<void> }[] = [
  { id: "libraries", label: "库管理", render: renderLibraries },
  { id: "compare", label: "对比预览", render: renderCompare },
  { id: "run", label: "同步执行", render: renderRun },
  { id: "settings", label: "设置", render: renderSettings },
];

let current: PageId = "libraries";

const $ = <T extends HTMLElement>(selector: string): T => document.querySelector<T>(selector)!;

async function main(): Promise<void> {
  const nav = $("#nav");
  for (const page of PAGES) {
    const btn = el("button", { class: `nav-btn${page.id === current ? " active" : ""}` }, [text(page.label)]);
    btn.dataset.page = page.id;
    btn.addEventListener("click", () => {
      void switchPage(page.id);
    });
    nav.append(btn);
  }

  // 进度事件统一订阅（M-2）：分发给同步执行页。
  events.syncProgress.on((ev) => {
    if (current === "run") {
      dispatchProgress(ev);
    }
  });

  await refreshSidebar();
  await switchPage("libraries");
}

async function switchPage(id: PageId): Promise<void> {
  current = id;
  detachProgress();
  for (const btn of Array.from(document.querySelectorAll<HTMLButtonElement>(".nav-btn"))) {
    btn.classList.toggle("active", btn.dataset.page === id);
  }
  const page = PAGES.find((p) => p.id === id);
  const content = $("#content");
  clear(content);
  if (page) {
    try {
      await page.render(content);
    } catch (err) {
      content.append(el("p", { class: "message message-error" }, [text(String(err))]));
    }
  }
  // 库管理页会改动注册表、执行页需要新鲜运行状态——到达这两页时刷新
  // 侧栏状态灯与全局警示条。
  if (id === "libraries" || id === "run") {
    await refreshSidebar();
  }
}

/** 侧栏状态灯 + 全局「游戏运行中」警示条（§6）。 */
async function refreshSidebar(): Promise<void> {
  const lights = $("#fork-lights");
  const warning = $("#global-warning");
  clear(lights);
  warning.classList.add("hidden");
  try {
    const cards = await LibraryService.list();
    let anyRunning = false;
    if (cards.length === 0) {
      lights.append(el("div", { class: "muted light-empty" }, [text("未注册 fork")]));
    }
    for (const card of cards) {
      const dot = el("span", { class: `fork-dot ${card.ProcessRunning ? "dot-running" : "dot-idle"}` });
      lights.append(el("div", { class: "light-row" }, [dot, el("span", { class: "light-name" }, [text(card.ForkID)])]));
      if (card.ProcessRunning) {
        anyRunning = true;
      }
    }
    if (anyRunning) {
      warning.textContent = "游戏运行中——收集与投放将被守卫拦截（S5：请先关闭游戏）";
      warning.classList.remove("hidden");
    }
  } catch {
    lights.append(el("div", { class: "muted light-empty" }, [text("状态不可用")]));
  }
}

void main();
