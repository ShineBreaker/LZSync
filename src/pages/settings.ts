// 设置页：语言、后端状态、存储路径、监控进程、链接支持。

import { getLang, setLang, t, trBackend, type Lang } from "../i18n";
import { SettingsService } from "../mygo";
import { clear, el, errText, message, text } from "../ui";

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
