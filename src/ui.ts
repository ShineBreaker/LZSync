// 通用 UI 助手（DESIGN.md §9）：DOM 构造、内联 SVG 图标、悬浮层、
// 右下角瞬态 toast、fork 色点。错误只走 toast/popover——页面上不放
// 解释句（§1）。

import { t, trBackend } from "./i18n";

// ---------- DOM ----------

export function el<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  attrs?: Record<string, string>,
  children?: (Node | string)[],
): HTMLElementTagNameMap[K] {
  const n = document.createElement(tag);
  if (attrs) {
    for (const [k, v] of Object.entries(attrs)) {
      if (k === "class") n.className = v;
      else n.setAttribute(k, v);
    }
  }
  if (children) {
    for (const c of children) n.append(c);
  }
  return n;
}

export function text(s: string): Text {
  return document.createTextNode(s);
}

export function clear(n: HTMLElement): void {
  while (n.firstChild) n.removeChild(n.firstChild);
}

export function errText(e: unknown): string {
  if (e instanceof Error) return e.message;
  return String(e);
}

// ---------- 图标（DESIGN.md §6.4 内联 SVG 几何图标） ----------

const ICONS: Record<string, string> = {
  chevron: `<svg viewBox="0 0 12 12" width="12" height="12"><path d="M3 4.5l3 3 3-3" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>`,
  x: `<svg viewBox="0 0 12 12" width="12" height="12"><path d="M3 3l6 6M9 3l-6 6" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>`,
  dots: `<svg viewBox="0 0 14 4" width="14" height="4"><circle cx="2" cy="2" r="1.4" fill="currentColor"/><circle cx="7" cy="2" r="1.4" fill="currentColor"/><circle cx="12" cy="2" r="1.4" fill="currentColor"/></svg>`,
  plus: `<svg viewBox="0 0 12 12" width="12" height="12"><path d="M6 2v8M2 6h8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>`,
  scan: `<svg viewBox="0 0 14 14" width="14" height="14"><circle cx="6" cy="6" r="4" fill="none" stroke="currentColor" stroke-width="1.5"/><path d="M9.2 9.2L12 12" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>`,
  folder: `<svg viewBox="0 0 14 12" width="14" height="12"><path d="M1 2.5A1.5 1.5 0 0 1 2.5 1h3l1.2 1.5h4.8A1.5 1.5 0 0 1 13 4v5.5A1.5 1.5 0 0 1 11.5 11h-9A1.5 1.5 0 0 1 1 9.5z" fill="none" stroke="currentColor" stroke-width="1.2"/></svg>`,
  check: `<svg viewBox="0 0 12 12" width="12" height="12"><path d="M2.5 6.5l2.4 2.4 4.6-5.3" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/></svg>`,
  lib: `<svg viewBox="0 0 14 14" width="14" height="14"><rect x="1.5" y="1.5" width="3.4" height="11" rx="1" fill="none" stroke="currentColor" stroke-width="1.3"/><rect x="5.6" y="1.5" width="3.4" height="11" rx="1" fill="none" stroke="currentColor" stroke-width="1.3"/><rect x="9.7" y="1.5" width="3.4" height="11" rx="1" fill="none" stroke="currentColor" stroke-width="1.3"/></svg>`,
};

export function icon(name: keyof typeof ICONS, cls = ""): HTMLElement {
  const span = el("span", { class: `ic ${cls}`.trim() });
  span.innerHTML = ICONS[name] ?? "";
  return span;
}

// ---------- fork 色彩（DESIGN.md §6.2） ----------

export const FORK_COLORS: Record<string, string> = {
  lazer: "#66CCFF",
  delta: "#C88BF5",
  ez2lazer: "#FF7EB6",
  g0v0: "#7CE8B8",
};

export function forkColor(forkID: string): string {
  return FORK_COLORS[forkID] ?? "#3A4150";
}

export function forkDot(forkID: string, size = 10): HTMLElement {
  const d = el("span", { class: "fdot" });
  d.style.background = forkColor(forkID);
  d.style.color = forkColor(forkID); // currentColor 供 .fdot.base 同色描边
  d.style.width = `${size}px`;
  d.style.height = `${size}px`;
  return d;
}

// ---------- 悬浮层（hover 覆盖点详情 / 「⋯」菜单） ----------

let activePopover: { node: HTMLElement; close: () => void } | null = null;

export function closePopover(): void {
  activePopover?.close();
}

/** showPopover 在 anchor 附近弹出内容；点击外部 / 再次调用 / anchor leave 关闭。
 *  mode="click"：点 anchor 开关、点击外部关（菜单）。
 *  mode="hover"：anchor hover 开、离开 anchor+popover 关（覆盖点详情）。 */
export function showPopover(anchor: HTMLElement, content: HTMLElement, mode: "click" | "hover"): void {
  closePopover();
  const pop = el("div", { class: "popover" });
  pop.append(content);
  document.body.append(pop);
  const r = anchor.getBoundingClientRect();
  pop.style.left = `${Math.min(r.left, window.innerWidth - 280)}px`;
  pop.style.top = `${r.bottom + 6}px`;
  let dead = false;
  const close = () => {
    if (dead) return;
    dead = true;
    pop.remove();
    document.removeEventListener("pointerdown", onDoc, true);
    anchor.removeEventListener("pointerleave", onLeave);
    pop.removeEventListener("pointerleave", onLeave);
    if (activePopover?.node === pop) activePopover = null;
  };
  const onDoc = (e: PointerEvent) => {
    if (!pop.contains(e.target as Node) && e.target !== anchor && !anchor.contains(e.target as Node)) {
      close();
    }
  };
  const onLeave = (e: PointerEvent) => {
    const to = e.relatedTarget as Node | null;
    if (to && (pop.contains(to) || anchor.contains(to))) return;
    close();
  };
  document.addEventListener("pointerdown", onDoc, true);
  if (mode === "hover") {
    anchor.addEventListener("pointerleave", onLeave);
    pop.addEventListener("pointerleave", onLeave);
  }
  activePopover = { node: pop, close };
}

// ---------- toast（右下角瞬态） ----------

export type ToastKind = "ok" | "err" | "info";

export function toast(kind: ToastKind, msg: string): void {
  let box = document.getElementById("toasts");
  if (!box) {
    box = el("div", { id: "toasts" });
    document.body.append(box);
  }
  const n = el("div", { class: `toast ${kind}` }, [text(trBackend(msg))]);
  box.append(n);
  setTimeout(() => {
    n.classList.add("out");
    setTimeout(() => n.remove(), 250);
  }, 3200);
}

// ---------- 数字/日期 ----------

export function fmtBytes(n: number): string {
  if (n >= 1 << 30) return `${(n / (1 << 30)).toFixed(1)}GiB`;
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(0)}MiB`;
  if (n >= 1 << 10) return `${(n / (1 << 10)).toFixed(0)}KiB`;
  return `${n}B`;
}

export function fmtDate(ms: number): string {
  const d = new Date(ms);
  const p = (x: number) => String(x).padStart(2, "0");
  return `${d.getFullYear()}/${p(d.getMonth() + 1)}/${p(d.getDate())}`;
}

export function fmtClock(ms: number): string {
  const d = new Date(ms);
  const p = (x: number) => String(x).padStart(2, "0");
  return `${p(d.getHours())}:${p(d.getMinutes())}`;
}

export function fmtMonthDay(ms: number): string {
  const d = new Date(ms);
  const p = (x: number) => String(x).padStart(2, "0");
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${fmtClock(ms)}`;
}

// ---------- 摘要拆行（"Artist - Title" → 副行/标题） ----------

export function splitSummary(summary: string): { title: string; sub: string } {
  const i = summary.indexOf(" - ");
  if (i <= 0) return { title: summary, sub: "" };
  return { title: summary.slice(i + 3), sub: summary.slice(0, i) };
}

// ---------- 覆盖态词表（§4.2 悬浮层词表） ----------

export function coverageWord(state: string, reason: string): string {
  switch (state) {
    case "has":
      return t("cov.has");
    case "incoming":
      return t("cov.incoming");
    case "conflict":
      return t("cov.conflict");
    case "confirm":
      return t("cov.confirm");
    case "deleted":
      return t("cov.deleted");
    default:
      if (reason === "不写入") return t("cov.noWrite");
      if (reason) return t("cov.unsupported");
      return t("cov.none");
  }
}
