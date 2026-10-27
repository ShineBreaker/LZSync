// 决策值卡（DESIGN.md §3.5/§4.4：收件箱与库行共享组件）：
// 冲突 → 逐端取值卡（缺席端置灰）+ 虚线「保留差异」卡；
// 待确认 → 单卡「更新为新版本」；删除行 → 单卡「恢复」。
// 中枢行不提供（§0-B12 后 UI 不再给「以库为准」选项）。

import { t } from "../i18n";
import type { CompareService } from "../mygo";
import { el, forkDot, text } from "../ui";

export type DecideAction =
  | { action: "winner"; winner: string }
  | { action: "select" };

type ConflictDetail = Awaited<ReturnType<typeof CompareService.unitConflict>>;

/** conflictCards 冲突展开卡集：逐端取值卡（缺席端置灰），选中态由
 * selected 驱动。baseline = 默认预选端（未拍板时弱化高亮，不写入
 * staged；用户点卡后才正式选中）。
 */
export function conflictCards(
  cd: ConflictDetail,
  onPick: (a: DecideAction) => void,
  selected?: string,
  baseline?: string,
): HTMLElement {
  const box = el("div", { class: "vcards" });
  for (const v of cd.Variants) {
    const isSel = selected === v.ForkID;
    const isPre = !selected && baseline === v.ForkID && v.Has;
    const card = el("div", {
      class: `vcard${v.Has ? "" : " disabled"}${isSel ? " sel" : ""}${isPre ? " pre" : ""}`,
      "data-fork": v.ForkID,
    });
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

/** singleCard 单卡处置（待确认「更新为新版本」/ 删除行「恢复」）。 */
