// 设置页：语言、同步范围、后端状态、存储路径、监控进程、链接支持。

import { getLang, setLang, t, trBackend, type Lang } from "../i18n";
import { SettingsService } from "../mygo";
import type { SyncScope } from "../mygo";
import { clear, el, errText, forkDot, message, text } from "../ui";

export async function renderSettings(root: HTMLElement): Promise<void> {
  clear(root);
  root.append(el("h1", { class: "page-title" }, [text(t("nav.settings"))]));

  const statusLine = el("div", { class: "status-line" });
  root.append(statusLine);

  const langSelect = el("select", { class: "input" }, [
    el("option", { value: "zh" }, [text("简体中文")]),
    el("option", { value: "en" }, [text("English")]),
  ]) as HTMLSelectElement;
  langSelect.value = getLang();
  langSelect.addEventListener("change", () => {
    setLang(langSelect.value as Lang);
  });
  root.append(section(t("set.language"), [row(t("set.language"), langSelect)]));

  // ---- 同步范围（§0-G1：板块开关 + 逐端拉取/写入角色） ----
  try {
    let scope = await SettingsService.getSyncScope();
    const save = (next: SyncScope, rollback: () => void): void => {
      void SettingsService.setSyncScope(next)
        .then(() => {
          scope = next; // 保存成功后以新态为基（否则连续改动互相回滚）
          clear(statusLine);
        })
        .catch((err) => {
          rollback();
          clear(statusLine);
          statusLine.append(message("error", errText(err)));
        });
    };
    const children: (HTMLElement | null)[] = [];
    for (const tg of scope.Sections) {
      const cb = el("input", { type: "checkbox" }) as HTMLInputElement;
      cb.checked = tg.Enabled;
      cb.addEventListener("change", () => {
        const next: SyncScope = {
          Sections: scope.Sections.map((s) =>
            s.Section === tg.Section ? { Section: s.Section, Enabled: cb.checked } : { ...s }),
          Forks: scope.Forks.map((f) => ({ ...f })),
        };
        const prev = !cb.checked;
        save(next, () => {
          cb.checked = prev;
        });
      });
      children.push(row(t(`sec.${tg.Section}` as never), cb));
    }
    for (const fr of scope.Forks) {
      const pull = el("input", { type: "checkbox" }) as HTMLInputElement;
      const write = el("input", { type: "checkbox" }) as HTMLInputElement;
      pull.checked = fr.Source;
      write.checked = fr.Target;
      const applyRole = (role: "Source" | "Target", val: boolean, cbEl: HTMLInputElement): void => {
        const next: SyncScope = {
          Sections: scope.Sections.map((s) => ({ ...s })),
          Forks: scope.Forks.map((f) =>
            f.ForkID === fr.ForkID ? { ...f, [role]: val } : { ...f }),
        };
        const prev = !val;
        save(next, () => {
          cbEl.checked = prev;
        });
      };
      pull.addEventListener("change", () => applyRole("Source", pull.checked, pull));
      write.addEventListener("change", () => applyRole("Target", write.checked, write));
      const forkRow = el("div", { class: "field" }, [
        el("span", { class: "label", style: "display:flex;align-items:center;gap:8px" }, [
          forkDot(fr.ForkID),
          text(fr.ForkID),
        ]),
        el("span", { class: "value", style: "display:flex;align-items:center;gap:14px" }, [
          el("label", { style: "display:flex;align-items:center;gap:6px" }, [pull, text(t("scope.pull"))]),
          el("label", { style: "display:flex;align-items:center;gap:6px" }, [write, text(t("scope.write"))]),
        ]),
      ]);
      children.push(forkRow);
    }
    root.append(section(t("set.syncScope"), children));
  } catch (err) {
    statusLine.append(message("error", errText(err)));
  }

  try {
    const info = await SettingsService.get();
    const modeBadge =
      info.Mode === "real"
        ? el("span", { class: "badge ok" }, [text(t("set.modeReal"))])
        : el("span", { class: "badge warn" }, [text(info.Mode === "mock" ? t("set.modeMock") : t("set.modeFallback"))]);

    root.append(section(t("set.backend"), [
      row(t("set.mode"), modeBadge),
      info.Reason ? row(t("set.mode"), document.createTextNode(trBackend(info.Reason)) as unknown as HTMLElement) : null,
      row(t("set.nativeLib"), el("span", { class: info.NativeLibOK ? "badge ok" : "badge err" }, [text(info.NativeLibOK ? t("set.nativeOk") : t("set.nativeFail"))])),
    ]));

    root.append(section(t("set.paths"), [
      row(t("set.dataDir"), el("code", {}, [text(info.DataDir)])),
      row(t("set.hubDb"), el("code", {}, [text(info.HubDBPath)])),
      row(t("set.backupsDir"), el("span", {}, [text(`${t("set.backupsCount", info.BackupsCount)} ${info.BackupsDir}`)])),
    ]));

    const guardList = el("ul", { style: "margin:0;padding-left:18px" });
    for (const name of info.GuardNames) {
      guardList.append(el("li", {}, [el("code", {}, [text(name)])]));
    }
    root.append(section(t("set.guardSection"), [guardList]));

    root.append(section(t("set.linkSection"), [
      row("Symlink", el("span", { class: info.LinkSymlink ? "badge ok" : "badge err" }, [text(info.LinkSymlink ? t("set.linkOk") : t("set.linkFail"))])),
      row("Hardlink", el("span", { class: info.LinkHardlink ? "badge ok" : "badge err" }, [text(info.LinkHardlink ? t("set.linkOk") : t("set.linkFail"))])),
    ]));
  } catch (err) {
    statusLine.append(message("error", errText(err)));
  }
}

function section(title: string, children: (HTMLElement | null)[]): HTMLElement {
  const box = el("section", { style: "margin-bottom:18px" });
  box.append(el("div", { class: "card-title", style: "margin-bottom:6px" }, [text(title)]));
  const card = el("div", { class: "card" });
  for (const child of children) {
    if (child) card.append(child);
  }
  box.append(card);
  return box;
}

function row(name: string, value: HTMLElement): HTMLElement {
  return el("div", { class: "field" }, [
    el("span", { class: "label" }, [text(name)]),
    el("span", { class: "value" }, [value]),
  ]);
}
