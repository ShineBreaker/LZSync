// 资料库页（DESIGN.md §4）：全量库行（canonical ∪ 差异键），一次
// CompareService.library(kind)。搜索/分段过滤/懒加载 80 行一批；
// 覆盖点悬浮详情；行内决策卡（decide.ts 共享组件）；勾演进暂存表。

import { t, trBackend } from "../i18n";
import { CompareService } from "../mygo";
import type { EntityKind, LibraryItem } from "../mygo";
import { onData, staged } from "../state";
import {
  clear,
  closePopover,
  coverageWord,
  el,
  errText,
  fmtDate,
  forkColor,
  forkDot,
  icon,
  showPopover,
  splitSummary,
  text,
  toast,
} from "../ui";
import { conflictCards, singleCard, type DecideAction } from "./decide";

const PAGE = 80;
const coverCache = new Map<string, string>();

type Seg = "all" | "pending" | "deleted";

/** renderLibrary 四数据页共用渲染。返回 cleanup。 */
export function renderLibrary(root: HTMLElement, kind: EntityKind): () => void {
  clear(root);
  const unsubs: (() => void)[] = [];
  let items: LibraryItem[] = [];
  let seg: Seg = "all";
  let query = "";
  let shown = 0;
  let observer: IntersectionObserver | null = null;

  const head = el("div", { class: "lib-head" });
  const listBox = el("div", { class: "lrows" });
  root.append(head, listBox);

  const filtered = (): LibraryItem[] => {
    const q = query.trim().toLowerCase();
    return items.filter((it) => {
      if (seg === "pending" && it.State !== "conflict" && it.State !== "confirm") return false;
      if (seg === "deleted" && !it.Deleted) return false;
      if (q && !it.Summary.toLowerCase().includes(q)) return false;
      return true;
    });
  };

  function paintHead(): void {
    clear(head);
    head.append(
      el("h1", {}, [text(t(`nav.${kind}`))]),
      el("span", { class: "count" }, [text(String(items.length))]),
      el("span", { class: "spacer" }),
    );
    const searchBox = el("div", { class: "lib-search" }, [
      icon("scan"),
      el("input", { placeholder: t("lib.search") }) as HTMLInputElement,
    ]);
    const input = searchBox.querySelector("input")!;
    input.value = query;
    input.addEventListener("input", () => {
      query = input.value;
      repaint();
    });
    head.append(searchBox);

    const pending = items.filter((i) => i.State === "conflict" || i.State === "confirm").length;
    const deleted = items.filter((i) => i.Deleted).length;
    const segs: { id: Seg; label: string; n: number }[] = (
      [
        { id: "all", label: t("lib.all"), n: items.length },
        { id: "pending", label: t("lib.pending"), n: pending },
        { id: "deleted", label: t("lib.deleted"), n: deleted },
      ] as { id: Seg; label: string; n: number }[]
    ).filter((s) => s.id === "all" || s.n > 0);
    if (segs.length > 1) {
      const segBox = el("div", { class: "seg" });
      for (const s of segs) {
        const b = el("button", { class: seg === s.id ? "on" : "" }, [text(`${s.label} ${s.n}`)]);
        b.addEventListener("click", () => {
          seg = s.id;
          paintHead();
          repaint();
        });
        segBox.append(b);
      }
      head.append(segBox);
    }
  }

  async function load(): Promise<void> {
    try {
      items = await CompareService.library(kind);
    } catch (e) {
      toast("err", errText(e));
      items = [];
    }
    paintHead();
    repaint();
  }

  function repaint(): void {
    clear(listBox);
    shown = 0;
    observer?.disconnect();
    appendChunk();
  }

  function appendChunk(): void {
    const rows = filtered();
    const next = Math.min(shown + PAGE, rows.length);
    for (let i = shown; i < next; i++) {
      const it = rows[i];
      if (it) listBox.append(rowEl(it));
    }
    shown = next;
    if (shown < rows.length) {
      const sentinel = el("div", { class: "lsentinel" });
      listBox.append(sentinel);
      observer = new IntersectionObserver((ents) => {
        if (ents.some((e) => e.isIntersecting)) {
          observer?.disconnect();
          sentinel.remove();
          appendChunk();
        }
      }, { root: document.getElementById("content"), rootMargin: "300px" });
      observer.observe(sentinel);
    }
  }

  // ---------- 行 ----------

  function rowEl(it: LibraryItem): HTMLElement {
    const tall = kind === "beatmap_set";
    const mini = kind === "score";
    const cls = [
      "lrow",
      tall ? "tall" : "",
      mini ? "mini" : "",
      it.State === "conflict" ? "conflict" : "",
      it.State === "confirm" ? "confirm" : "",
      it.Deleted ? "deleted" : "",
      staged.get(it.Kind, it.ContentKey) ? "staged" : "",
    ].filter(Boolean).join(" ");
    const row = el("div", { class: cls });

    const check = el("input", { type: "checkbox", class: "lcheck" }) as HTMLInputElement;
    check.checked = staged.get(it.Kind, it.ContentKey) !== undefined;
    check.addEventListener("click", (e) => e.stopPropagation());
    check.addEventListener("change", () => {
      if (check.checked) {
        staged.put({ kind: it.Kind, contentKey: it.ContentKey, label: it.Summary || it.ContentKey, action: "select", winner: "" });
        row.classList.add("staged");
      } else {
        staged.remove(it.Kind, it.ContentKey);
        row.classList.remove("staged");
      }
      listBox.classList.toggle("any-checked", staged.size > 0);
    });

    const main = el("div", { class: "lmain" });
    const { title, sub } = splitSummary(it.Summary || it.ContentKey);
    main.append(el("div", { class: "ltitle" }, [text(title)]));
    if (sub) main.append(el("div", { class: "lsub" }, [text(sub)]));
    const meta = el("div", { class: "lmeta" });
    const oid = onlineID(it.IdentityKey);
    if (oid) meta.append(el("span", {}, [text(`#${oid}`)]));
    if (it.Deleted && it.DeletedAt > 0) {
      meta.append(el("span", {}, [text(t("lib.deletedAt", fmtDate(it.DeletedAt)))]));
    }
    if (meta.childNodes.length) main.append(meta);

    const cov = el("div", { class: "lcov" });
    for (const f of it.Forks) {
      const d = el("span", { class: `cov-dot ${f.State}` });
      if (f.State === "has" || f.State === "incoming") {
        d.style.background = f.State === "has" ? forkColor(f.ForkID) : "transparent";
        d.style.borderColor = forkColor(f.ForkID);
        d.style.color = forkColor(f.ForkID);
      }
      cov.append(d);
    }
    cov.addEventListener("mouseenter", () => {
      showPopover(cov, covPopover(it), "hover");
    });
    cov.addEventListener("click", (e) => e.stopPropagation());

    row.append(el("span", { class: "lbar" }), check, main, cov);

    if (tall) {
      const cover = el("span", { class: "lcover" });
      row.prepend(cover);
      void coverFor(it.ContentKey).then((url) => {
        if (url) {
          cover.style.backgroundImage = `url(${JSON.stringify(url)})`;
          row.classList.add("has-cover");
        }
      });
    }

    // 行内决策卡：conflict/confirm 行点开；deleted 行给「恢复」卡。
    const actionable = it.State === "conflict" || it.State === "confirm" || it.Deleted;
    if (actionable) {
      const detail = el("div", { class: "ldetail hidden" });
      row.append(detail);
      let loaded = false;
      row.style.cursor = "pointer";
      row.addEventListener("click", () => {
        const nowHidden = detail.classList.toggle("hidden");
        if (loaded || nowHidden) return;
        loaded = true;
        if (it.State === "conflict") {
          CompareService.unitConflict(it.Kind, it.ContentKey)
            .then((cd) => {
              clear(detail);
              detail.append(conflictCards(cd, (a) => pick(a)));
            })
            .catch((e) => {
              clear(detail);
              toast("err", errText(e));
            });
        } else {
          detail.append(singleCard(it.Deleted && it.State === "ok" ? t("decide.restore") : t("decide.update"), (a) => pick(a)));
        }
      });
      const pick = (a: DecideAction): void => {
        if (a.action === "winner") {
          staged.put({ kind: it.Kind, contentKey: it.ContentKey, label: it.Summary || it.ContentKey, action: "winner", winner: a.winner });
        } else {
          staged.put({ kind: it.Kind, contentKey: it.ContentKey, label: it.Summary || it.ContentKey, action: "select", winner: "" });
        }
        row.classList.add("staged");
        check.checked = true;
        listBox.classList.toggle("any-checked", staged.size > 0);
        detail.classList.add("hidden");
      };
    }
    return row;
  }

  function covPopover(it: LibraryItem): HTMLElement {
    const box = el("div");
    for (const f of it.Forks) {
      const word = coverageWord(f.State, f.Reason);
      const wordCls = f.State === "has" ? "ok" : f.State === "confirm" ? "warn" : f.State === "conflict" ? "err" : "";
      box.append(
        el("div", { class: "p-row" }, [
          forkDot(f.ForkID, 8),
          el("span", { class: "p-name" }, [text(f.ForkID)]),
          el("span", { class: `p-word ${wordCls}` }, [text(word)]),
        ]),
      );
      if (f.Reason && f.State !== "conflict") {
        box.append(el("div", { class: "p-reason" }, [text(trBackend(f.Reason))]));
      }
    }
    return box;
  }

  async function coverFor(key: string): Promise<string> {
    const hit = coverCache.get(key);
    if (hit !== undefined) return hit;
    const url = await CompareService.coverImage(key).catch(() => "");
    coverCache.set(key, url);
    return url;
  }

  function onlineID(ik: string): string {
    const [p, v] = ik.split(":");
    if (p !== "online" || v === "-1" || v === "0" || !v) return "";
    return v;
  }

  listBox.classList.toggle("any-checked", staged.size > 0);
  unsubs.push(onData(() => void load()));
  unsubs.push(staged.onChange(() => {
    listBox.classList.toggle("any-checked", staged.size > 0);
  }));
  unsubs.push(() => {
    observer?.disconnect();
    closePopover();
  });

  void load();
  return () => unsubs.forEach((u) => u());
}
