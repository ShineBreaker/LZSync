// 库管理页（§6.1）：自动发现扫描、fork 卡片（身份、版本来源标注、files
// 体积、进程运行状态）、手动注册（识别失败要求指认）、移除（不碰磁盘）。

import { t, trBackend } from "../i18n";
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
  root.append(el("h1", { class: "page-title" }, [text(t("nav.libraries"))]));

  // 工具行：扫描 + 手动注册。
  const scanBtn = el("button", { class: "btn btn-primary" }, [text(t("lib.scan"))]);
  const pathInput = el("input", {
    class: "input",
    placeholder: t("lib.pathPlaceholder"),
  });
  const regBtn = el("button", { class: "btn" }, [text(t("lib.register"))]);
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
        el("p", { class: "muted" }, [text(t("lib.empty"))]),
      );
      return;
    }
    for (const card of cards) {
      cardsBox.append(renderCard(card));
    }
  }

  function renderCard(card: Awaited<ReturnType<typeof LibraryService.list>>[number]): HTMLElement {
    const badges = [
      el("span", { class: "badge badge-version" }, [text(t("lib.version", trBackend(card.VersionLabel)))]),
      el("span", { class: "badge badge-source" }, [text(versionSourceLabel(String(card.VersionSource)))]),
    ];
    if (card.ProcessRunning) {
      badges.push(el("span", { class: "badge badge-running" }, [text(t("lib.running", card.RunningNames.join(", ")))]));
    } else {
      badges.push(el("span", { class: "badge badge-idle" }, [text(t("lib.idle"))]));
    }
    const removeBtn = el("button", { class: "btn btn-danger" }, [text(t("lib.remove"))]);
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
        kv(t("lib.dataDir"), card.DataDir),
        kv(t("lib.realmFile"), `${card.RealmPath} (${fmtBytes(card.RealmSize)})`),
        kv(t("lib.filesDir"), `${card.FilesDir} · ${t("lib.filesCount", card.FilesCount)} · ${fmtBytes(card.FilesSize)}`),
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
      await refresh();
      statusLine.append(message("ok", t("lib.scanDone", refs.length)));
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
      statusLine.append(message("info", t("lib.enterPath")));
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
        await refresh();
        statusLine.append(message("ok", t("lib.registered", ref.ForkID, ref.DiskVersion)));
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
    const btn = el("button", { class: "btn btn-primary" }, [text(t("lib.confirmAssign"))]);
    btn.addEventListener("click", async () => {
      if (!pending) return;
      btn.disabled = true;
      try {
        const value = select.value;
        const ref = await LibraryService.assignFork(pending.path, value);
        pending = null;
        await refresh();
        statusLine.append(message("ok", t("lib.assigned", ref.ForkID)));
      } catch (err) {
        clear(statusLine);
        statusLine.append(message("error", errText(err)));
        btn.disabled = false;
      }
    });
    statusLine.append(
      el("div", { class: "assign-box" }, [
        el("span", {}, [text(t("lib.unidentified", pending.diskVersion || t("lib.unknownVer")))]),
        select,
        btn,
      ]),
    );
  }

  await refresh();
}
