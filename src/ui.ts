// UI 基础设施：DOM 构造助手与展示格式化（vanilla TS strict，M-3）。

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
  return new Date(ms).toLocaleString();
}

/** 分类徽标文本（§6.2 差异分类徽标）。 */
export function categoryLabel(cat: string): string {
  switch (cat) {
    case "create":
      return "新增";
    case "target-only":
      return "目标独有";
    case "conflict":
      return "冲突";
    case "version-update":
      return "版本更新";
    case "restore":
      return "恢复（已删除）";
    default:
      return cat;
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
      return "现场开库探测";
    case "filename":
      return "文件名推断";
    case "snapshot-cache":
      return "结构档案缓存";
    case "unknown-running":
      return "未知（运行中）";
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
    return err.message;
  }
  return String(err);
}

/** 清空容器。 */
export function clear(node: HTMLElement): void {
  while (node.firstChild) {
    node.removeChild(node.firstChild);
  }
}
