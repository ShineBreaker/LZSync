// UI 基础设施：DOM 构造助手与展示格式化（vanilla TS strict，M-3）。

import { getLang, t, trBackend } from "./i18n";

/** 类型安全的元素构造：el("div", { class: "card", text: "hi" }, [child…])。 */
export function el<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  attrs?: Partial<Record<string, string | boolean>>,
  children?: Node[],
): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  if (attrs) {
    for (const [key, value] of Object.entries(attrs)) {
      if (value === true) {
        node.setAttribute(key, "");
      } else if (typeof value === "string") {
        node.setAttribute(key, value);
      }
    }
  }
  if (children) {
    for (const child of children) {
      node.append(child);
    }
  }
  return node;
}

/** 文本节点快捷方式。 */
export function text(s: string): Text {
  return document.createTextNode(s);
}

/** 字节量人类可读化。 */
export function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["KiB", "MiB", "GiB", "TiB"] as const;
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(1)} ${units[i] ?? "PiB"}`;
}

/** Unix 毫秒时间戳本地化显示。 */
export function fmtMillis(ms: number): string {
  if (ms <= 0) return "—";
  return new Date(ms).toLocaleString(getLang() === "en" ? "en-US" : "zh-CN");
}

/** 分类徽标文本（§6.2 差异分类徽标）。 */
export function categoryLabel(cat: string): string {
  switch (cat) {
    case "create":
    case "target-only":
    case "conflict":
    case "version-update":
    case "restore":
      return t(`cat.${cat}`);
    default:
      return cat;
  }
}

/** 实体类型显示名（§6.2 数据类型 Tab / 行键显示）。 */
export function kindLabel(kind: string): string {
  switch (kind) {
    case "beatmap_set":
    case "beatmap":
    case "collection":
    case "skin":
    case "score":
    case "key_binding":
    case "ruleset_setting":
    case "mod_preset":
      return t(`kind.${kind}`);
    default:
      return kind;
  }
}

/** 计划动作显示名（§6.2 计划摘要 / §6.3 执行报告）。 */
export function actionLabel(action: string): string {
  switch (action) {
    case "create":
    case "update":
    case "version-replace":
      return t(`action.${action}`);
    default:
      return action;
  }
}

/** 徽标色类。 */
export function categoryClass(cat: string): string {
  return `badge badge-${cat}`;
}

/** 版本来源标注（§3.2 降级链）。 */
export function versionSourceLabel(src: string): string {
  switch (src) {
    case "realm-open":
    case "filename":
    case "snapshot-cache":
    case "unknown-running":
      return t(`vsrc.${src}`);
    default:
      return src;
  }
}

/** 行内状态提示条：show(el) 渲染错误/成功消息。 */
export function message(kind: "error" | "ok" | "info", msg: unknown): HTMLElement {
  const node = el("div", { class: `message message-${kind}` });
  node.textContent = String(msg);
  return node;
}

/** 错误对象 → 展示文本（mygo 以错误消息字符串拒绝 Promise）。 */
export function errText(err: unknown): string {
  if (err instanceof Error) {
    return trBackend(err.message);
  }
  return trBackend(String(err));
}

/** 清空容器。 */
export function clear(node: HTMLElement): void {
  while (node.firstChild) {
    node.removeChild(node.firstChild);
  }
}

/* ===== 数据浏览页专用辅助（DESIGN.md §数据行 / §悬浮层） ===== */

/** 同步状态语义（对应 DESIGN.md §同步状态语义色）。 */
export type SyncState = "synced" | "update" | "conflict" | "needsConfirm" | "targetOnly";

export function syncStateLabel(state: SyncState): string {
  return t(`st.${state}`);
}

export function syncStateClass(state: SyncState): string {
  switch (state) {
    case "synced":
      return "st-ok";
    case "update":
      return "st-update";
    case "conflict":
      return "st-err";
    case "needsConfirm":
      return "st-warn";
    case "targetOnly":
      return "st-neutral";
  }
}

/** fork 色点（DESIGN.md §四端指示色只作 8px 圆点）。 */
export function forkDot(forkID: string): HTMLElement {
  const dot = el("span", { class: "dot" });
  dot.style.background = `var(--fork-${forkID}, var(--fork-unknown))`;
  return dot;
}

/** 悬浮层端行：`色点 + 端名 + 状态`（DESIGN.md §悬浮明细）。 */
export function popoverForkRow(forkID: string, state: "ok" | "warn" | "err" | "missing", label: string): HTMLElement {
  const row = el("div", { class: "popover-row" });
  row.append(forkDot(forkID), el("span", { class: "name" }, [text(forkID)]), el("span", { class: `state ${state}` }, [text(label)]));
  return row;
}

/** 悬浮层定位在父元素下方右对齐（数据行 x/y 块悬浮）。 */
export function popoverAt(trigger: HTMLElement, build: (pop: HTMLElement) => void): () => void {
  let pop: HTMLElement | null = null;

  function open(): void {
    if (pop) return;
    pop = el("div", { class: "popover" });
    build(pop);
    // popover 挂到 body + fixed 定位，避免 .data-row 的 overflow:hidden 裁切（DESIGN.md §悬浮明细）。
    // 先插入再测量宽度。
    document.body.append(pop);
    const rect = trigger.getBoundingClientRect();
    const pw = pop.offsetWidth;
    pop.style.top = `${rect.bottom + 6}px`;
    let left = rect.right - pw;
    if (left + pw > window.innerWidth - 8) left = window.innerWidth - pw - 8;
    if (left < 8) left = 8;
    pop.style.left = `${left}px`;
  }

  function close(): void {
    if (pop) {
      pop.remove();
      pop = null;
    }
  }

  trigger.addEventListener("mouseenter", open);
  trigger.addEventListener("mouseleave", close);
  trigger.addEventListener("click", open);
  return close;
}

