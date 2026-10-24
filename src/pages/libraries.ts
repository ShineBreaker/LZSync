// 库管理页（§6.1）：自动发现扫描、fork 卡片（身份、版本来源标注、files
// 体积、进程运行状态）、手动注册（识别失败要求指认）、移除（不碰磁盘）。

import { LibraryService } from "../mygo";
import { clear, el, errText, fmtBytes, message, text, versionSourceLabel } from "../ui";

/** 四端指认候选（forks 注册表全集）。 */
const FORK_CHOICES = ["lazer", "delta", "ez2lazer", "g0v0"] as const;

/** 待指认的未识别库（识别失败分支的 UI 状态）。 */
interface PendingAssign {
  path: string;
  diskVersion: number;
}

export async function renderLibraries(root: HTMLElement): Promise<void> {
  clear(root);
  root.append(el("h1", { class: "page-title" }, [text("库管理")]));

  // 工具行：扫描 + 手动注册。
  const scanBtn = el("button", { class: "btn btn-primary" }, [text("扫描数据根")]);
  const pathInput = el("input", {
    class: "input",
    placeholder: "自定义库文件路径（storage.ini 场景，指向 client.realm）",
  });
  const regBtn = el("button", { class: "btn" }, [text("注册")]);
  const toolbar = el("div", { class: "toolbar" }, [pathInput, regBtn, scanBtn]);
  const statusLine = el("div", { class: "status-line" });
  root.append(toolbar, statusLine);

  const cardsBox = el("div", { class: "cards" });
  root.append(cardsBox);

  let pending: PendingAssign | null = null;

  async function refresh(): Promise<void> {
    clear(statusLine);
    try {
      const cards = await LibraryService.list();
      renderCards(cards);
    } catch (err) {
      statusLine.append(message("error", errText(err)));
    }
  }

  function renderCards(cards: Awaited<ReturnType<typeof LibraryService.list>>): void {
    clear(cardsBox);
    if (cards.length === 0) {
      cardsBox.append(
        el("p", { class: "muted" }, [text("尚未发现或注册任何 fork 库——点击「扫描数据根」或手动注册。")]),
      );
      return;
    }
    for (const card of cards) {
      cardsBox.append(renderCard(card));
    }
  }

  function renderCard(card: Awaited<ReturnType<typeof LibraryService.list>>[number]): HTMLElement {
    const badges = [
      el("span", { class: "badge badge-version" }, [text(`磁盘版本 ${card.VersionLabel}`)]),
      el("span", { class: "badge badge-source" }, [text(versionSourceLabel(String(card.VersionSource)))]),
    ];
    if (card.ProcessRunning) {
      badges.push(el("span", { class: "badge badge-running" }, [text(`运行中（${card.RunningNames.join("、")}）`)]));
    } else {
      badges.push(el("span", { class: "badge badge-idle" }, [text("未运行")]));
    }
    const removeBtn = el("button", { class: "btn btn-danger" }, [text("解除管理")]);
    removeBtn.addEventListener("click", async () => {
      removeBtn.disabled = true;
      try {
        await LibraryService.remove(card.ForkID);
        await refresh();
      } catch (err) {
        removeBtn.disabled = false;
        clear(statusLine);
        statusLine.append(message("error", errText(err)));
      }
    });
    return el("div", { class: "card fork-card" }, [
      el("div", { class: "fork-card-head" }, [
        el("span", { class: `fork-dot ${card.ProcessRunning ? "dot-running" : "dot-idle"}` }),
        el("span", { class: "fork-name" }, [text(card.ForkID)]),
        ...badges,
        el("span", { class: "spacer" }),
        removeBtn,
      ]),
      el("dl", { class: "kv" }, [
        kv("数据目录", card.DataDir),
        kv("库文件", `${card.RealmPath}（${fmtBytes(card.RealmSize)}）`),
        kv("files/", `${card.FilesDir} · ${card.FilesCount} 项 · ${fmtBytes(card.FilesSize)}`),
        kv(
          "版本解码",
          card.ForkRev > 0
            ? `官方基线 ${card.UpstreamVersion} + fork 修订 ${card.ForkRev}`
            : `官方基线 ${card.UpstreamVersion}`,
        ),
      ]),
    ]);
  }

  function kv(term: string, desc: string): HTMLElement {
    return el("div", { class: "kv-row" }, [
      el("dt", {}, [text(term)]),
      el("dd", {}, [text(desc)]),
    ]);
  }

  scanBtn.addEventListener("click", async () => {
    scanBtn.disabled = true;
    clear(statusLine);
    try {
      const refs = await LibraryService.scan();
      statusLine.append(message("ok", `扫描完成：发现 ${refs.length} 个 fork 库`));
      await refresh();
    } catch (err) {
      statusLine.append(message("error", errText(err)));
    } finally {
      scanBtn.disabled = false;
    }
  });

  regBtn.addEventListener("click", async () => {
    const path = pathInput.value.trim();
    if (!path) {
      clear(statusLine);
      statusLine.append(message("info", "请输入库文件路径"));
      return;
    }
    regBtn.disabled = true;
    clear(statusLine);
    try {
      const ref = await LibraryService.register(path);
      if (ref.ForkID === "") {
        // 识别失败：呈现原始版本并要求用户指认（绝不猜，§6.1）。
        pending = { path, diskVersion: ref.DiskVersion };
        renderAssign();
      } else {
        pending = null;
        statusLine.append(message("ok", `已注册 ${ref.ForkID}（版本 ${ref.DiskVersion}）`));
        await refresh();
      }
    } catch (err) {
      statusLine.append(message("error", errText(err)));
    } finally {
      regBtn.disabled = false;
    }
  });

  /** 指认面板：选择 fork 后 AssignFork。 */
  function renderAssign(): void {
    clear(statusLine);
    if (!pending) return;
    const select = el("select", { class: "input" });
    for (const id of FORK_CHOICES) {
      const opt = el("option", { value: id }, [text(id)]);
      select.append(opt);
    }
    const btn = el("button", { class: "btn btn-primary" }, [text("确认指认")]);
    btn.addEventListener("click", async () => {
      if (!pending) return;
      btn.disabled = true;
      try {
        const value = select.value;
        const ref = await LibraryService.assignFork(pending.path, value);
        pending = null;
        clear(statusLine);
        statusLine.append(message("ok", `已按指认注册 ${ref.ForkID}`));
        await refresh();
      } catch (err) {
        clear(statusLine);
        statusLine.append(message("error", errText(err)));
        btn.disabled = false;
      }
    });
    statusLine.append(
      el("div", { class: "assign-box" }, [
        el("span", {}, [text(`无法识别该库（原始版本 ${pending.diskVersion || "未知"}）——请指认 fork：`)]),
        select,
        btn,
      ]),
    );
  }

  await refresh();
}
