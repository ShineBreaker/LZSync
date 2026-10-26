// 客户端页（DESIGN.md §客户端）：客户端只是数据来源，行列表而非卡片。
// 支持注册/移除/识别失败指认。

import { t, trBackend } from "../i18n";
import { LibraryService } from "../mygo";
import { clear, el, errText, fmtBytes, forkDot, message, text, versionSourceLabel } from "../ui";

const FORK_CHOICES = ["lazer", "delta", "ez2lazer", "g0v0"] as const;

interface PendingAssign {
  path: string;
  diskVersion: number;
}

export async function renderClients(root: HTMLElement): Promise<void> {
  clear(root);
  root.append(el("h1", { class: "page-title" }, [text(t("clients.title"))]));

  const toolbar = el("div", { class: "toolbar" });
  const pathInput = el("input", { class: "input", placeholder: t("clients.pathPlaceholder"), style: "flex:1" });
  const regBtn = el("button", { class: "btn" }, [text(t("clients.add"))]);
  const scanBtn = el("button", { class: "btn primary" }, [text(t("clients.scan"))]);
  toolbar.append(pathInput, regBtn, scanBtn);
  const statusLine = el("div", { class: "status-line" });
  const listBox = el("div");
  root.append(toolbar, statusLine, listBox);

  let pending: PendingAssign | null = null;

  async function refresh(): Promise<void> {
    clear(statusLine);
    try {
      const cards = await LibraryService.list();
      renderList(cards);
    } catch (err) {
      statusLine.append(message("error", errText(err)));
    }
  }

  function renderList(cards: Awaited<ReturnType<typeof LibraryService.list>>): void {
    clear(listBox);
    if (cards.length === 0) {
      listBox.append(el("p", { class: "page-meta" }, [text(t("clients.empty"))]));
      return;
    }
    for (const card of cards) {
      listBox.append(renderRow(card));
    }
    if (pending) renderAssign();
  }

  function renderRow(card: Awaited<ReturnType<typeof LibraryService.list>>[number]): HTMLElement {
    const row = el("div", { class: "card", style: "display:flex;align-items:center;gap:12px;padding:10px 14px;margin-bottom:6px" });
    const isUnidentified = card.ForkID === "" || !card.Registered;
    if (card.ProcessRunning) {
      row.style.borderColor = "var(--warn)";
    }

    // 主信息
    const main = el("div", { style: "flex:1;min-width:0" });
    main.append(
      el("div", { style: "display:flex;align-items:center;gap:8px" }, [
        forkDot(card.ForkID || "unknown"),
        el("span", { style: "font-size:13px;font-weight:700" }, [text(card.ForkID || t("clients.unidentified"))]),
        ...(card.ProcessRunning ? [el("span", { class: "badge warn" }, [text(t("clients.running"))])] : []),
      ]),
      el("div", { class: "page-meta", style: "margin-top:4px" }, [
        text(`${card.DataDir} · ${t("clients.version", card.DiskVersion)} · ${fmtBytes(card.FilesSize)} · ${versionSourceLabel(String(card.VersionSource))}`),
      ]),
    );

    row.append(main);

    // 右侧操作
    if (isUnidentified) {
      const sel = el("select", { class: "input", style: "width:120px" }) as HTMLSelectElement;
      for (const id of FORK_CHOICES) sel.append(el("option", { value: id }, [text(id)]));
      const btn = el("button", { class: "btn primary" }, [text(t("clients.confirmAssign"))]);
      btn.addEventListener("click", async () => {
        btn.disabled = true;
        try {
          await LibraryService.assignFork(card.RealmPath, sel.value);
          pending = null;
          await refresh();
        } catch (err) {
          statusLine.append(message("error", errText(err)));
          btn.disabled = false;
        }
      });
      row.append(sel, btn);
    } else {
      const rmBtn = el("button", { class: "btn danger" }, [text(t("clients.remove"))]);
      rmBtn.disabled = card.ProcessRunning;
      rmBtn.addEventListener("click", async () => {
        rmBtn.disabled = true;
        try {
          await LibraryService.remove(card.ForkID);
          await refresh();
        } catch (err) {
          statusLine.append(message("error", errText(err)));
          rmBtn.disabled = false;
        }
      });
      row.append(rmBtn);
    }
    return row;
  }

  function renderAssign(): void {
    if (!pending) return;
    const sel = el("select", { class: "input" }) as HTMLSelectElement;
    for (const id of FORK_CHOICES) sel.append(el("option", { value: id }, [text(id)]));
    const btn = el("button", { class: "btn primary" }, [text(t("clients.confirmAssign"))]);
    btn.addEventListener("click", async () => {
      if (!pending) return;
      btn.disabled = true;
      try {
        await LibraryService.assignFork(pending.path, sel.value);
        pending = null;
        await refresh();
        statusLine.append(message("ok", t("clients.added", sel.value)));
      } catch (err) {
        statusLine.append(message("error", errText(err)));
        btn.disabled = false;
      }
    });
    statusLine.append(
      el("div", { class: "message warn" }, [
        text(`${pending.path} · ${t("clients.version", pending.diskVersion)} → `),
        sel,
        btn,
      ]),
    );
  }

  scanBtn.addEventListener("click", async () => {
    scanBtn.disabled = true;
    clear(statusLine);
    try {
      await LibraryService.scan();
      await refresh();
    } catch (err) {
      statusLine.append(message("error", errText(err)));
    } finally {
      scanBtn.disabled = false;
    }
  });

  regBtn.addEventListener("click", async () => {
    const path = pathInput.value.trim();
    if (!path) return;
    regBtn.disabled = true;
    clear(statusLine);
    try {
      const ref = await LibraryService.register(path);
      if (ref.ForkID === "") {
        // 用后端解析后的库文件路径指认：输入若是数据目录，Register 已定位到
        // 其内库文件，不能把目录当库文件再交给 AssignFork。
        pending = { path: ref.RealmPath, diskVersion: ref.DiskVersion };
        renderAssign();
      } else {
        pending = null;
        await refresh();
        statusLine.append(message("ok", t("clients.added", ref.ForkID)));
      }
    } catch (err) {
      statusLine.append(message("error", errText(err)));
    } finally {
      regBtn.disabled = false;
    }
  });

  await refresh();
}
