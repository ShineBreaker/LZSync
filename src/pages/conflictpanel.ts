// 冲突明细二级面板（数据页与冲突页共用）：中枢值 + 逐端取值/字段差异 +
// 「以中枢为准 / 以此端为准」处置按钮。CanPickHub=false 的类（game.ini）
// 中枢行仅展示不供选择。

import { t } from "../i18n";
import type { CompareService } from "../mygo";
import { el, text } from "../ui";

type ConflictDetail = Awaited<ReturnType<typeof CompareService.unitConflict>>;
type FieldDiffRow = { Field: string; HubValue: string; ForkValue: string };

/** 字段差异兜底盘（unitConflict 取不到时的老行为）。 */
export function conflictFallback(fields: FieldDiffRow[] | undefined): HTMLElement {
  if (!fields || fields.length === 0) {
    return el("div", { class: "page-meta" }, [text(t("browse.conflictNoData"))]);
  }
  const table = el("table", { class: "row-detail-table" });
  table.append(el("tr", {}, [
    el("th", {}, [text("")]),
    el("th", {}, [text(t("browse.conflictHub"))]),
    el("th", {}, [text(t("browse.conflictFork"))]),
  ]));
  for (const f of fields) {
    table.append(el("tr", {}, [
      el("td", { class: "k" }, [text(f.Field)]),
      el("td", {}, [text(f.HubValue)]),
      el("td", {}, [text(f.ForkValue)]),
    ]));
  }
  return table;
}

/** 逐端取值面板：onPick(winner) — "" = 以中枢为准，否则该端 ForkID。 */
export function conflictDetail(cd: ConflictDetail, onPick: (winner: string) => void): HTMLElement {
  const table = el("table", { class: "row-detail-table" });
  table.append(el("tr", {}, [
    el("th", {}, [text("")]),
    el("th", {}, [text(t("browse.conflictValue"))]),
    el("th", {}, [text(t("browse.conflictDiff"))]),
    el("th", {}, [text("")]),
  ]));
  const btn = (winner: string, label: string) => {
    const b = el("button", { class: "btn small" }, [text(label)]) as HTMLButtonElement;
    b.addEventListener("click", (e) => {
      e.stopPropagation();
      onPick(winner);
    });
    return b;
  };
  table.append(el("tr", {}, [
    el("td", { class: "k" }, [text(t("browse.conflictHub"))]),
    el("td", {}, [text(cd.Canonical || "—")]),
    el("td", {}, [text("")]),
    el("td", {}, cd.CanPickHub ? [btn("", t("browse.useHub"))] : []),
  ]));
  for (const v of cd.Variants) {
    const diffs = (v.Fields ?? []).map((f) => `${f.Field}: ${f.HubValue} → ${f.ForkValue}`).join("；");
    table.append(el("tr", {}, [
      el("td", { class: "k" }, [text(v.ForkID)]),
      el("td", {}, [text(v.Value || t("browse.variantMissing"))]),
      el("td", { class: "k" }, [text(diffs)]),
      el("td", {}, v.Has ? [btn(v.ForkID, t("browse.useFork"))] : []),
    ]));
  }
  const box = el("div");
  box.append(table, el("div", { class: "page-meta" }, [text(t("browse.hintResolve"))]));
  return box;
}
