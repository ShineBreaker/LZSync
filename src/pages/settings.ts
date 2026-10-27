// 设置页（DESIGN.md §8）：语言 / 存储 / 引擎 / 链接支持 / 守卫名单——
// 行式布局，无同步范围（角色与板块开关都在同步台就地操作）。

import { getLang, setLang, t } from "../i18n";
import { SettingsService } from "../mygo";
import type { SettingsInfo } from "../mygo";
import { clear, el, errText, text, toast } from "../ui";

export function renderSettings(root: HTMLElement): () => void {
  clear(root);
  root.append(el("h1", { class: "page-title" }, [text(t("set.title"))]));
  const list = el("div", { class: "set-list" });
  root.append(list);

  function row(name: string, val: string, act?: HTMLElement): HTMLElement {
    const r = el("div", { class: "srow" });
    r.append(el("span", { class: "sname" }, [text(name)]));
    r.append(el("span", { class: "sval" }, [text(val)]));
    if (act) r.append(el("span", { class: "sact" }, [act]));
    return r;
  }

  function paint(info: SettingsInfo): void {
    clear(list);

    // 语言
    const sel = el("select", { class: "input" }) as HTMLSelectElement;
    sel.append(
      el("option", { value: "zh" }, [text("中文")]),
      el("option", { value: "en" }, [text("English")]),
    );
    sel.value = getLang();
    sel.addEventListener("change", () => {
      setLang(sel.value as "zh" | "en");
      window.dispatchEvent(new Event("lzsync:lang"));
    });
    list.append(row(t("set.lang"), "", sel));

    // 基准端（收件箱外环 + 冲突处置默认参照；默认 lazer）
    const blSel = el("select", { class: "input" }) as HTMLSelectElement;
    void SettingsService.getSyncScope()
      .then((scope) => {
        for (const fr of scope.Forks) {
          blSel.append(el("option", { value: fr.ForkID }, [text(fr.ForkID)]));
        }
        blSel.value = info.BaselineFork || "lazer";
      })
      .catch(() => undefined);
    blSel.addEventListener("change", () => {
      void SettingsService.setBaseline(blSel.value)
        .then(() => toast("ok", t("set.baselineSaved")))
        .catch((e) => toast("err", errText(e)));
    });
    list.append(row(t("set.baseline"), "", blSel));

    // 存储
    list.append(row(t("set.dataDir"), info.DataDir));
    list.append(row(t("set.hubDb"), info.HubDBPath));
    list.append(row(t("set.backups"), info.BackupsDir, el("span", { class: "count" }, [text(t("set.backupsCount", info.BackupsCount))])));

    // 引擎：状态词一行，原始原因进悬浮提示。
    const modeKey = info.Mode === "real" ? "set.engine.real" : info.Mode === "mock" ? "set.engine.mock" : "set.engine.fallback";
    const engVal = el("span", { class: "sval" });
    engVal.title = info.Reason || info.NativeLibErr || "";
    engVal.append(text(t(modeKey)));
    const engRow = el("div", { class: "srow" });
    engRow.append(el("span", { class: "sname" }, [text(t("set.engine"))]), engVal);
    list.append(engRow);

    // 链接支持：能力片 + 说明进悬浮。
    const linkRow = el("div", { class: "srow" });
    linkRow.title = info.LinkNote || "";
    const linkChips = el("div", { class: "svals" });
    linkChips.append(
      el("span", { class: `chip${info.LinkSymlink ? " on" : " off"}` }, [text(`${t("set.sym")} ${info.LinkSymlink ? "✓" : "✕"}`)]),
      el("span", { class: `chip${info.LinkHardlink ? " on" : " off"}` }, [text(`${t("set.hard")} ${info.LinkHardlink ? "✓" : "✕"}`)]),
    );
    linkRow.append(el("span", { class: "sname" }, [text(t("set.links"))]), linkChips);
    list.append(linkRow);

    // 守卫名单
    const chips = el("div", { class: "svals" });
    for (const n of info.GuardNames) {
      chips.append(el("span", { class: "sec" }, [text(n)]));
    }
    const guardRow = el("div", { class: "srow" });
    guardRow.append(el("span", { class: "sname" }, [text(t("set.guard"))]), chips);
    list.append(guardRow);
  }

  void SettingsService.get()
    .then(paint)
    .catch((e) => toast("err", errText(e)));

  return () => undefined;
}
