// 决策值卡（DESIGN.md §3.5/§4.4：收件箱与库行共享组件）：
// 冲突 → 逐端取值卡（缺席端置灰）+ 虚线「保留差异」卡；
// 待确认 → 单卡「更新为新版本」；删除行 → 单卡「恢复」。
// 中枢行不提供（§0-B12 后 UI 不再给「以库为准」选项）。

import { t, trBackend } from "../i18n";
import type { CompareService } from "../mygo";
import { el, forkDot, text } from "../ui";

export type DecideAction =
  | { action: "winner"; winner: string }
  | { action: "keep" }
  | { action: "select" };

type ConflictDetail = Awaited<ReturnType<typeof CompareService.unitConflict>>;

/** conflictCards 冲突展开卡集：每变体一卡 + 「保留差异」虚线卡；
 * selected = 已拍板 winner（加 .sel 选中态）。 */
export function conflictCards(cd: ConflictDetail, onPick: (a: DecideAction) => void, selected?: string): HTMLElement {
  const box = el("div", { class: "vcards" });
  for (const v of cd.Variants) {
    const isSel = selected === v.ForkID;
    const card = el("div", { class: `vcard${v.Has ? "" : " disabled"}${isSel ? " sel" : ""}` });
    card.append(el("div", { class: "vhead" }, [forkDot(v.ForkID), text(v.ForkID)]));
    card.append(el("div", { class: "vval" }, [text(v.Value || "—")]));
    if (v.Has) {
      card.addEventListener("click", (e) => {
        e.stopPropagation();
        onPick({ action: "winner", winner: v.ForkID });
      });
    }
    box.append(card);
  }
  const keep = el("div", { class: "vcard keep" });
  keep.append(el("div", { class: "vhead" }, [text(t("decide.keep"))]));
  keep.addEventListener("click", (e) => {
    e.stopPropagation();
    onPick({ action: "keep" });
  });
  box.append(keep);
  return box;
}

/** singleCard 单卡处置（待确认「更新为新版本」/ 删除行「恢复」）。 */
export function singleCard(label: string, onPick: (a: DecideAction) => void): HTMLElement {
  const box = el("div", { class: "vcards" });
  const card = el("div", { class: "vcard single" });
  card.append(el("div", { class: "vhead" }, [text(label)]));
  card.addEventListener("click", (e) => {
    e.stopPropagation();
    onPick({ action: "select" });
  });
  box.append(card);
  return box;
}

/** reasonLine 后端原因行（悬浮层/决策卡下的原因附行，可省）。 */
export function reasonLine(reason: string): HTMLElement {
  return el("div", { class: "vdiffs" }, [text(trBackend(reason))]);
}
