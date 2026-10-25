// 设置页（§6.4）：界面语言切换、后端与 Realm 引擎状态、存储目录、监控进程与链接支持。

import { getLang, setLang, t, trBackend, type Lang } from "../i18n";
import { SettingsService } from "../mygo";
import { clear, el, errText, message, text } from "../ui";

export async function renderSettings(root: HTMLElement): Promise<void> {
  clear(root);
  root.append(el("h1", { class: "page-title" }, [text(t("nav.settings"))]));

  const statusLine = el("div", { class: "status-line" });
  root.append(statusLine);

  const langSelect = el("select", { class: "input input-s" }, [
    el("option", { value: "zh" }, [text("简体中文")]),
    el("option", { value: "en" }, [text("English")]),
  ]) as HTMLSelectElement;
  langSelect.value = getLang();
  langSelect.addEventListener("change", () => {
    setLang(langSelect.value as Lang);
  });
  root.append(section(t("set.langSection"), [
    row(t("set.langLabel"), "", langSelect),
  ]));

  try {
    const info = await SettingsService.get();
    const modeBadge =
      info.Mode === "real"
        ? el("span", { class: "badge badge-idle" }, [text(t("set.modeReal"))])
        : el("span", { class: "badge badge-running" }, [
            text(info.Mode === "mock" ? t("set.modeMock") : t("set.modeFallback")),
          ]);

    root.append(section(t("set.backendSection"), [
      row(t("set.mode"), "", modeBadge),
      info.Reason ? row(t("set.reason"), trBackend(info.Reason)) : null,
      row(
        t("set.nativeLib"),
        info.NativeLibOK ? t("set.nativeOk") : t("set.nativeFail", trBackend(info.NativeLibErr)),
      ),
    ]));

    root.append(section(t("set.pathsSection"), [
      row(t("set.dataDir"), info.DataDir),
      row(t("set.hubDb"), info.HubDBPath),
      row(t("set.backupsDir"), t("set.backupsCount", info.BackupsDir, info.BackupsCount)),
    ]));

    const guardList = el("ul", { class: "guard-list" });
    for (const name of info.GuardNames) {
      guardList.append(el("li", {}, [text(name)]));
    }
    root.append(section(t("set.guardSection"), [
      guardList,
    ]));

    root.append(section(t("set.linkSection"), [
      row(t("set.symlink"), info.LinkSymlink ? t("set.linkOk") : t("set.linkFail")),
      row(t("set.hardlink"), info.LinkHardlink ? t("set.linkOk") : t("set.linkFail")),
    ]));
  } catch (err) {
    statusLine.append(message("error", errText(err)));
  }
}

function section(title: string, children: (HTMLElement | null)[]): HTMLElement {
  const box = el("section", { class: "section" }, [el("h2", {}, [text(title)])]);
  for (const child of children) {
    if (child) {
      box.append(child);
    }
  }
  return box;
}

function row(name: string, desc: string, extra?: HTMLElement): HTMLElement {
  const children: Node[] = [el("span", { class: "check-name" }, [text(name)])];
  if (extra) {
    children.push(extra);
  }
  if (desc) {
    children.push(el("span", { class: "muted" }, [text(desc)]));
  }
  return el("div", { class: "check-row" }, children);
}
