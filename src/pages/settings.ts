// 设置页（§6.4 M1 骨架，只读显示）：后端模式与 native 诊断、hub 数据目录、
// 备份目录、守卫名单、链接策略探测。

import { SettingsService } from "../mygo";
import { clear, el, errText, message, text } from "../ui";

export async function renderSettings(root: HTMLElement): Promise<void> {
  clear(root);
  root.append(el("h1", { class: "page-title" }, [text("设置")]));

  const statusLine = el("div", { class: "status-line" });
  root.append(statusLine);

  try {
    const info = await SettingsService.get();
    const modeBadge =
      info.Mode === "real"
        ? el("span", { class: "badge badge-idle" }, [text("真实后端")])
        : el("span", { class: "badge badge-running" }, [text(info.Mode === "mock" ? "Mock 后端（强制）" : "Mock 后端（降级）")]);

    root.append(section("后端", [
      row("模式", "", modeBadge),
      info.Reason ? row("说明", info.Reason) : null,
      row(
        "native 库",
        info.NativeLibOK ? "已加载（librealm 20.1.x 对齐）" : `不可用：${info.NativeLibErr}`,
      ),
    ]));

    root.append(section("hub 数据目录（§3.4）", [
      row("数据目录", info.DataDir),
      row("hub.db", info.HubDBPath),
      row("备份目录", `${info.BackupsDir}（现有 ${info.BackupsCount} 份）`),
    ]));

    const guardList = el("ul", { class: "guard-list" });
    for (const name of info.GuardNames) {
      guardList.append(el("li", {}, [text(name)]));
    }
    root.append(section("进程守卫名单（内置 + 用户扩展）", [
      guardList,
      el("p", { class: "muted" }, [
        text("注意：g0v0 的进程名带感叹号（g0v0!），匹配时不可去掉；delta 进程名待运行时枚举后由用户补充（T2）。备份不可禁用、最多改位置（S2）。"),
      ]),
    ]));

    root.append(section("链接策略（§5.3）", [
      row("符号链接", info.LinkSymlink ? "可用（首选，跨卷）" : "不可用 → 降级硬链接"),
      row("硬链接", info.LinkHardlink ? "可用（同卷）" : "不可用 → 降级复制"),
      row("策略说明", info.LinkNote),
    ]));

    root.append(
      el("p", { class: "muted" }, [
        text("M1 设置骨架：以上为只读显示。备份保留策略、冲突方向、批大小高级项、仓迁移与 game.ini 白名单管理待 M2/M3 落地。"),
      ]),
    );
  } catch (err) {
    statusLine.append(message("error", errText(err)));
  }
}

function section(title: string, children: (HTMLElement | null)[]): HTMLElement {
  const box = el("section", { class: "section" }, [el("h2", {}, [text(title)])]);
  for (const child of children) {
    if (child) {
      box.append(child);
    }
  }
  return box;
}

function row(name: string, desc: string, extra?: HTMLElement): HTMLElement {
  const children: Node[] = [el("span", { class: "check-name" }, [text(name)])];
  if (extra) {
    children.push(extra);
  }
  if (desc) {
    children.push(el("span", { class: "muted" }, [text(desc)]));
  }
  return el("div", { class: "check-row" }, children);
}
