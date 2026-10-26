// 冲突处置页：全类冲突清单（CompareService.listConflicts）——数据页只
// 展示四类实体，键位/规则集设置/mod 预设/game.ini 的冲突在此统一处置。
// 展开行 = 逐端取值面板（共享 conflictpanel）；拍板后「同步所选」走
// PrepareSelected → 同步页确认执行（与数据页勾选项同管道）。

import { t } from "../i18n";
import { CompareService, SyncService } from "../mygo";
import { conflictDetail } from "./conflictpanel";
import { setPendingPreview } from "./sync";
import { clear, el, errText, message, navigate, text } from "../ui";

type ConflictRow = Awaited<ReturnType<typeof CompareService.listConflicts>>[number];

export async function renderConflicts(root: HTMLElement): Promise<void> {
  clear(root);
  root.append(el("h1", { class: "page-title" }, [text(t("nav.conflicts"))]));

  const statusLine = el("div", { class: "status-line" });
  const toolbar = el("div", { class: "toolbar" });
  const listBox = el("div");
  root.append(toolbar, statusLine, listBox);

  const checked = new Set<string>();
  const resolutions = new Map<string, string>(); // rowKey → winner（"" = 中枢）

  const syncBtn = el("button", { class: "btn primary" }, [text(t("conflicts.syncBtn"))]);
  const metaEl = el("span", { class: "page-meta" }, [text("…")]);
  toolbar.append(metaEl, el("span", { class: "spacer" }), syncBtn);

  statusLine.append(message("info", t("browse.loading")));

  let rows: ConflictRow[] = [];
  try {
    rows = await CompareService.listConflicts();
  } catch (err) {
    clear(statusLine);
    statusLine.append(message("error", errText(err)));
    return;
  }
  clear(statusLine);

  const key = (r: ConflictRow) => `${r.Kind}${r.ContentKey}`;
  const updateMeta = () => {
    metaEl.textContent = `${t("conflicts.total", rows.length)} · ${t("browse.selected", checked.size)}`;
  };
  updateMeta();

  syncBtn.addEventListener("click", async () => {
    if (checked.size === 0) {
      statusLine.append(message("info", t("sync.noPlan")));
      return;
    }
    syncBtn.disabled = true;
    clear(statusLine);
    try {
      const sel = rows.filter((r) => checked.has(key(r))).map((r) => ({
        Kind: r.Kind as never, ContentKey: r.ContentKey, Winner: resolutions.get(key(r)) ?? "",
      }));
      const preview = await SyncService.prepareSelected(sel);
      setPendingPreview(preview);
      navigate("sync");
    } catch (err) {
      statusLine.append(message("error", errText(err)));
    } finally {
      syncBtn.disabled = false;
    }
  });

  if (rows.length === 0) {
    listBox.append(el("p", { class: "page-meta" }, [text(t("conflicts.empty"))]));
    return;
  }

  for (const r of rows) {
    listBox.append(renderRow(r));
  }

  function renderRow(r: ConflictRow): HTMLElement {
    const rk = key(r);
    const rowEl = el("div", { class: "data-row compact err-row" });

    const cb = el("input", { type: "checkbox", class: "row-check" }) as HTMLInputElement;
    cb.checked = checked.has(rk);
    rowEl.classList.toggle("checked", cb.checked);
    cb.addEventListener("change", () => {
      if (cb.checked) checked.add(rk);
      else checked.delete(rk);
      rowEl.classList.toggle("checked", cb.checked);
      updateMeta();
    });

    const resolveBadge = el("span", { class: "resolve-badge" });
    const sub = `${t(`kind.${r.Kind}` as never)} · ${t("conflicts.holders", r.Forks.join("、"))} · ${t("conflicts.values", r.Values)}`;
    const main = el("div", { class: "row-main" }, [
      el("div", { class: "row-title" }, [text(r.Label || r.ContentKey)]),
      el("div", { class: "row-meta" }, [el("span", {}, [text(sub)]), resolveBadge]),
    ]);
    if (resolutions.has(rk)) {
      const w = resolutions.get(rk)!;
      resolveBadge.textContent = w ? `→ ${w}` : t("browse.pickedHub");
    }

    const pick = (winner: string) => {
      resolutions.set(rk, winner);
      checked.add(rk);
      cb.checked = true;
      rowEl.classList.add("checked");
      resolveBadge.textContent = winner ? `→ ${winner}` : t("browse.pickedHub");
      updateMeta();
    };

    const detail = el("div", { class: "row-detail hidden" });
    detail.append(el("div", { class: "page-meta" }, [text(t("browse.conflictLoading"))]));
    rowEl.append(detail);
    rowEl.style.cursor = "pointer";
    let loaded = false;
    rowEl.addEventListener("click", (e) => {
      if ((e.target as HTMLElement).closest("input[type=checkbox]")) return;
      detail.classList.toggle("hidden");
      if (loaded || detail.classList.contains("hidden")) return;
      loaded = true;
      CompareService.unitConflict(r.Kind as never, r.ContentKey).then((cd) => {
        clear(detail);
        detail.append(conflictDetail(cd, pick));
      }).catch((err) => {
        clear(detail);
        detail.append(message("error", errText(err)));
      });
    });

    rowEl.append(cb, main);
    return rowEl;
  }
}
