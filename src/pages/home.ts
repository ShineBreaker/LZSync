// 同步台（DESIGN.md §3）：客户端列 | 连线列 | 库卡，下面是待处理收件箱。
// 两条轨道：上 = 拉取（Source）、下 = 写入（Target）——点击即开关、立即
// 保存（失败回滚 + toast）。一轮运行态经 state.ts 的 run 机驱动连线填充
// 与库卡按钮二步确认。

import iconUrl from "../../resources/icon.png";
import { kindLabel, t, trBackend } from "../i18n";
import {
  CompareService,
  DialogService,
  LibraryService,
  SettingsService,
  SyncService,
} from "../mygo";
import type {
  EntityKind,
  ForkRole,
  ForkRunReport,
  LibraryCard,
  SyncScope,
} from "../mygo";
import {
  addPendingClient,
  bumpData,
  cancelRun,
  confirmRun,
  dropPendingClient,
  onData,
  onRun,
  pendingClients,
  runView,
  staged,
  startAll,
} from "../state";
import {
  clear,
  closePopover,
  el,
  errText,
  fmtBytes,
  fmtMonthDay,
  forkDot,
  icon,
  showPopover,
  text,
  toast,
} from "../ui";
import { conflictCards, singleCard } from "./decide";

const KIND_ORDER: EntityKind[] = ["beatmap_set", "collection", "skin", "score"];
const SECTION_COUNT: Record<string, string> = {
  beatmap_set: "beatmap_set",
  collection: "collection",
  skin: "skin",
  score: "score",
};

let scanTried = false;

/** renderHome 同步台页。返回 cleanup（切页时退订运行态/数据刷新）。 */
export function renderHome(root: HTMLElement): () => void {
  clear(root);
  const unsubs: (() => void)[] = [];

  const topo = el("div", { class: "topo" });
  const inboxBox = el("div", { class: "inbox hidden" });
  root.append(topo, inboxBox);

  // 运行态依赖的 DOM 引用（重画时更新，不重建）。
  let hubBtn: HTMLButtonElement = el("button");
  let hubCancel: HTMLButtonElement = el("button");
  let hubDiscard: HTMLButtonElement = el("button");
  const lanes = new Map<string, { lane: HTMLElement; pullFill: HTMLElement; writeFill: HTMLElement; pill: HTMLElement }>();
  let cards: LibraryCard[] = [];
  let scope: SyncScope | null = null;
  let counts: Record<string, number> = {};
  let settingsDir = "";

  // ---------- 数据装载 ----------

  async function loadAll(): Promise<void> {
    try {
      [cards, scope, counts] = await Promise.all([
        LibraryService.list().then((c) => [...c].sort((a, b) => a.ForkID.localeCompare(b.ForkID))),
        SettingsService.getSyncScope(),
        CompareService.hubCounts(),
      ]);
      SettingsService.get().then((s) => (settingsDir = s.BackupsDir)).catch(() => undefined);
      paintTopo();
      void loadInbox();
      if (cards.length === 0 && !scanTried) {
        scanTried = true;
        void LibraryService.scan().then(loadAll).catch(() => undefined);
      }
    } catch (e) {
      toast("err", errText(e));
    }
  }

  // ---------- 拓扑 ----------

  function roleOf(forkID: string): ForkRole | undefined {
    return scope?.Forks.find((f) => f.ForkID === forkID);
  }

  async function saveScope(next: SyncScope): Promise<boolean> {
    try {
      await SettingsService.setSyncScope(next);
      scope = next;
      return true;
    } catch (e) {
      toast("err", errText(e));
      return false;
    }
  }

  function cloneScope(): SyncScope | null {
    if (!scope) return null;
    return {
      Sections: scope.Sections.map((s) => ({ ...s })),
      Forks: scope.Forks.map((f) => ({ ...f })),
    };
  }

  function paintTopo(): void {
    clear(topo);
    lanes.clear();
    const rows = el("div", { class: "topo-rows" });
    for (const card of cards) {
      rows.append(rowOf(clientNode(card), laneNode(card.ForkID)));
    }
    for (const p of pendingClients) {
      rows.append(rowOf(pendingNode(p), el("div", { class: "lane" })));
    }
    rows.append(rowOf(addNode(), el("div", { class: "lane" })));
    topo.append(rows, hubCard());
    repaintRun();
  }

  function rowOf(client: HTMLElement, lane: HTMLElement): HTMLElement {
    const row = el("div", { class: "trow" });
    row.append(client, lane);
    return row;
  }

  function laneNode(forkID: string): HTMLElement {
    const lane = el("div", { class: "lane" });
    const pull = track(forkID, "pull");
    const write = track(forkID, "write");
    lane.append(pull.track, write.track);
    lanes.set(forkID, { lane, pullFill: pull.fill, writeFill: write.fill, pill: write.pill });
    return lane;
  }

  function track(forkID: string, dir: "pull" | "write"): { track: HTMLElement; fill: HTMLElement; pill: HTMLElement } {
    const tr = el("div", { class: `track ${dir}`, title: t(dir === "pull" ? "lane.pull" : "lane.write") });
    const line = el("i", { class: "tl" });
    const head = el("i", { class: "th" });
    const fill = el("i", { class: "tf" });
    const pill = el("span", { class: "pill hidden" });
    tr.append(line, head, fill, pill);
    tr.addEventListener("click", async () => {
      const next = cloneScope();
      const fr = next?.Forks.find((f) => f.ForkID === forkID);
      if (!next || !fr) return;
      if (dir === "pull") fr.Source = !fr.Source;
      else fr.Target = !fr.Target;
      if (await saveScope(next)) {
        tr.classList.toggle("off", dir === "pull" ? !fr.Source : !fr.Target);
        repaintRun();
      }
    });
    const r = roleOf(forkID);
    tr.classList.toggle("off", dir === "pull" ? !(r?.Source ?? true) : !(r?.Target ?? true));
    return { track: tr, fill, pill };
  }

  // ---------- 客户端节点 ----------

  function clientNode(card: LibraryCard): HTMLElement {
    const node = el("div", { class: `client${card.ProcessRunning ? " running" : ""}` });
    const dot = el("span", { class: "cdot" }, [forkDot(card.ForkID, 12)]);
    const main = el("div", { class: "cmain" });
    const ver = card.UpstreamVersion > 0 ? card.UpstreamVersion : card.DiskVersion;
    const sub = `${ver > 0 ? `v${ver} · ` : ""}${fmtBytes(card.FilesSize)}`;
    main.append(
      el("div", { class: "cname" }, [text(card.ForkID)]),
      el("div", { class: `csub${card.ProcessRunning ? " running" : ""}` }, [
        text(card.ProcessRunning ? `${sub} · ${t("client.running")}` : sub),
      ]),
    );
    const menu = el("span", { class: "cmenu" }, [icon("dots")]);
    menu.addEventListener("click", (e) => {
      e.stopPropagation();
      clientMenu(menu, card, node);
    });
    node.append(dot, main, menu);
    return node;
  }

  function pendingNode(p: { realmPath: string; diskVersion: number }): HTMLElement {
    const node = el("div", { class: "client unidentified" });
    const name = p.realmPath.split("/").pop() ?? p.realmPath;
    const idents = el("div", { class: "csub ident" });
    for (const id of ["delta", "ez2lazer", "g0v0", "lazer"]) {
      const d = forkDot(id, 9);
      d.style.cursor = "pointer";
      d.title = id;
      d.addEventListener("click", async (e) => {
        e.stopPropagation();
        try {
          await LibraryService.assignFork(p.realmPath, id);
          dropPendingClient(p.realmPath);
          toast("ok", t("toast.assigned"));
          await loadAll();
        } catch (err) {
          toast("err", errText(err));
        }
      });
      idents.append(d);
    }
    node.append(
      el("span", { class: "qdot" }, [text("?")]),
      el("div", { class: "cmain" }, [
        el("div", { class: "cname" }, [text(name)]),
        idents,
      ]),
    );
    return node;
  }

  function addNode(): HTMLElement {
    const node = el("div", { class: "client add" }, [icon("plus")]);
    node.addEventListener("click", () => {
      const menu = el("div");
      menu.append(
        menuItem(t("client.scan"), "scan", async () => {
          closePopover();
          try {
            await LibraryService.scan();
            await loadAll();
          } catch (e) {
            toast("err", errText(e));
          }
        }),
        menuItem(t("client.pickDir"), "folder", async () => {
          closePopover();
          try {
            const path = await DialogService.pickFolder("");
            if (path) await registerPath(path);
          } catch (e) {
            toast("err", errText(e));
          }
        }),
      );
      showPopover(node, menu, "click");
    });
    return node;
  }

  async function registerPath(path: string): Promise<void> {
    try {
      const ref = await LibraryService.register(path);
      if (ref.ForkID === "") {
        addPendingClient({ realmPath: ref.RealmPath, diskVersion: ref.DiskVersion });
      } else {
        toast("ok", t("toast.registered", ref.ForkID));
      }
      await loadAll();
    } catch (e) {
      toast("err", errText(e));
    }
  }

  function menuItem(label: string, ic: "scan" | "folder", fn: () => void): HTMLElement {
    const n = el("div", { class: "p-item" }, [icon(ic), text(label)]);
    n.addEventListener("click", (e) => {
      e.stopPropagation();
      fn();
    });
    return n;
  }

  function clientMenu(anchor: HTMLElement, card: LibraryCard, node: HTMLElement): void {
    const menu = el("div");
    const restore = el("div", { class: "p-item" }, [text(t("client.restore"))]);
    restore.addEventListener("click", async () => {
      closePopover();
      try {
        const path = await DialogService.pickFile(settingsDir);
        if (path) twoStepRestore(node, card, path);
      } catch (e) {
        toast("err", errText(e));
      }
    });
    const remove = el("div", { class: "p-item danger" }, [text(t("client.remove"))]);
    remove.addEventListener("click", () => {
      closePopover();
      twoStepRemove(node, card);
    });
    menu.append(restore, remove);
    showPopover(anchor, menu, "click");
  }

  /** twoStep 就地两步确认：标签 + [动作][✕]，确认跑 fn、✕ 恢复节点。 */
  function twoStep(node: HTMLElement, label: string, btn: string, danger: boolean, fn: () => Promise<void>): void {
    const main = node.querySelector<HTMLElement>(".cmain");
    if (!main) return;
    const saved = [...main.childNodes];
    clear(main);
    const go = el("button", { class: `btn small${danger ? " danger" : " primary"}` }, [text(btn)]);
    const undo = el("button", { class: "btn icon" }, [icon("x")]);
    undo.addEventListener("click", (e) => {
      e.stopPropagation();
      clear(main);
      main.append(...saved);
    });
    go.addEventListener("click", async (e) => {
      e.stopPropagation();
      go.disabled = true;
      try {
        await fn();
        clear(main);
        main.append(...saved);
      } catch (err) {
        toast("err", errText(err));
        go.disabled = false;
      }
    });
    main.append(
      el("div", { class: "cname", style: "font-weight:600" }, [text(label)]),
      el("div", { style: "display:flex;gap:6px;margin-top:6px" }, [go, undo]),
    );
  }

  function twoStepRestore(node: HTMLElement, card: LibraryCard, path: string): void {
    const m = /\.(\d{8})T(\d{6})\.(\d{3})Z$/.exec(path);
    let label = path.split("/").pop() ?? path;
    if (m) {
      const d = new Date(`${m[1]!.slice(0, 4)}-${m[1]!.slice(4, 6)}-${m[1]!.slice(6, 8)}T${m[2]!.slice(0, 2)}:${m[2]!.slice(2, 4)}:${m[2]!.slice(4, 6)}.${m[3]!}Z`);
      if (!isNaN(d.getTime())) label = fmtMonthDay(d.getTime());
    }
    twoStep(node, t("client.restoreAt", label), t("decide.restore"), false, async () => {
      await SyncService.restoreBackup(card.ForkID, path);
      toast("ok", t("toast.restored"));
      await loadAll();
    });
  }

  function twoStepRemove(node: HTMLElement, card: LibraryCard): void {
    twoStep(node, t("client.remove"), t("client.remove"), true, async () => {
      await LibraryService.remove(card.ForkID);
      toast("ok", t("toast.removed"));
      await loadAll();
    });
  }

  // ---------- 库卡 ----------

  function hubCard(): HTMLElement {
    const hub = el("div", { class: "hub" });
    const iconImg = el("img", { src: iconUrl, alt: "LZSync" }) as HTMLImageElement;
    hub.append(el("span", { class: "hicon" }, [iconImg]));
    const secs = el("div", { class: "secs" });
    for (const s of scope?.Sections ?? []) {
      const countKey = SECTION_COUNT[s.Section];
      const chip = el("span", { class: `sec${s.Enabled ? "" : " off"}` });
      chip.append(text(kindLabel(s.Section)));
      if (countKey !== undefined) {
        chip.append(el("span", { class: "n" }, [text(String(counts[countKey] ?? 0))]));
      }
      chip.addEventListener("click", async () => {
        const next = cloneScope();
        const tgt = next?.Sections.find((x) => x.Section === s.Section);
        if (!next || !tgt) return;
        tgt.Enabled = !tgt.Enabled;
        if (await saveScope(next)) chip.classList.toggle("off", !tgt.Enabled);
      });
      secs.append(chip);
    }
    hub.append(secs);

    const btns = el("div", { class: "hbtns" });
    hubBtn = el("button", { class: "btn main primary" }, [text(t("btn.sync"))]);
    hubBtn.addEventListener("click", () => {
      const v = runView();
      if (v.phase === "idle" || v.phase === "done") {
        void startAll();
      } else if (v.phase === "ready" && v.total > 0) {
        void confirmRun();
      }
    });
    // 拉取/写入中 → ✕ 中断；就绪 → 次按钮「取消」放弃本轮预览。
    hubCancel = el("button", { class: "btn icon hidden", title: t("btn.cancel") }, [icon("x")]);
    hubCancel.addEventListener("click", () => cancelRun());
    hubDiscard = el("button", { class: "btn hidden" }, [text(t("btn.cancel"))]);
    hubDiscard.addEventListener("click", () => cancelRun());
    btns.append(hubBtn, hubCancel, hubDiscard);
    hub.append(btns);
    return hub;
  }

  // ---------- 运行态重画 ----------

  function repaintRun(): void {
    const v = runView();
    const anyTarget = (scope?.Forks ?? []).some((f) => f.Target);
    const noClients = cards.length === 0 && pendingClients.length === 0;

    // 主按钮：同步 → 拉取中… → 写入 N 项 → 写入中… → 回到「同步」（§3.2 步序）。
    hubCancel.classList.toggle("hidden", v.phase !== "preparing" && v.phase !== "committing");
    hubDiscard.classList.toggle("hidden", !(v.phase === "ready" && v.total > 0));
    switch (v.phase) {
      case "ready":
        if (v.total > 0) {
          hubBtn.textContent = t("btn.writeN", v.total);
          hubBtn.className = "btn main primary";
          hubBtn.disabled = false;
        } else {
          hubBtn.textContent = t("btn.uptodate");
          hubBtn.className = "btn main ok";
          hubBtn.disabled = true;
        }
        break;
      case "preparing":
        hubBtn.textContent = t("btn.pulling");
        hubBtn.className = "btn main";
        hubBtn.disabled = true;
        break;
      case "committing":
        hubBtn.textContent = t("btn.writing");
        hubBtn.className = "btn main primary";
        hubBtn.disabled = true;
        break;
      default:
        hubBtn.textContent = t("btn.sync");
        hubBtn.className = "btn main primary";
        hubBtn.disabled = noClients || !anyTarget;
    }

    // 连线：跳过端 40%；轨道填充按 run.lanes；胶囊按阶段。
    const skipped = new Map((v.preview?.Skipped ?? []).map((s) => [s.ForkID, s.Reason]));
    const plusN = new Map((v.preview?.Targets ?? []).map((tp) => [tp.ForkID, tp.Items]));
    const reports = new Map((v.report?.Forks ?? []).map((fr) => [fr.ForkID, fr]));
    for (const [fid, ln] of lanes) {
      const rv = v.lanes[fid];
      ln.pullFill.style.width = `${Math.round((rv?.pull ?? 0) * 100)}%`;
      ln.writeFill.style.width = `${Math.round((rv?.write ?? 0) * 100)}%`;
      ln.lane.classList.toggle("skipped", skipped.has(fid) || reports.get(fid)?.Skipped === true);
      const pill = ln.pill;
      if (v.phase === "ready" && v.total > 0 && (plusN.get(fid) ?? 0) > 0) {
        pill.textContent = `+${plusN.get(fid)}`;
        pill.className = "pill";
        pill.onmouseenter = null;
      } else if (v.phase === "done" && reports.has(fid)) {
        const fr = reports.get(fid)!;
        if (fr.Skipped) {
          pill.className = "pill hidden";
        } else if (fr.Error || !fr.Deploy) {
          pill.textContent = "✕";
          pill.className = "pill err";
          pill.onmouseenter = () => showPopover(pill, reportPopover(fr, v.report?.Pending.Unsupported ?? 0), "hover");
        } else {
          const n = fr.Deploy.Applied?.length ?? 0;
          pill.textContent = `✓ ${n}`;
          pill.className = "pill ok";
          pill.onmouseenter = () => showPopover(pill, reportPopover(fr, v.report?.Pending.Unsupported ?? 0), "hover");
        }
      } else {
        pill.className = "pill hidden";
        pill.onmouseenter = null;
      }
    }
  }

  function reportPopover(fr: ForkRunReport, unsupported: number): HTMLElement {
    const box = el("div");
    if (fr.Error) {
      box.append(el("div", { class: "p-reason", style: "padding-left:0;color:var(--danger)" }, [text(trBackend(fr.Error))]));
    }
    if (fr.Deploy) {
      let created = 0;
      let updated = 0;
      for (const a of fr.Deploy.Applied ?? []) {
        if (a.Action === "create") created++;
        else updated++;
      }
      let skipped = 0;
      for (const s of fr.Deploy.Skipped ?? []) skipped += s.Count;
      box.append(
        el("div", { class: "p-row" }, [
          el("span", { class: "p-word" }, [text(`${t("result.created", created)} · ${t("result.updated", updated)} · ${t("result.skipped", skipped)}`)]),
        ]),
      );
      const backup = fr.Deploy.BackupPath || fr.Deploy.GameIni?.BackupPath || "";
      if (backup) {
        box.append(el("div", { class: "p-reason", style: "padding-left:0" }, [text(t("result.backup", backup.split("/").pop() ?? backup))]));
      }
    }
    if (unsupported > 0) {
      box.append(el("div", { class: "p-reason", style: "padding-left:0" }, [text(t("pend.unsupported", unsupported))]));
    }
    return box;
  }

  // ---------- 待处理收件箱（§3.5） ----------

  interface InboxItem {
    mode: "conflict" | "confirm";
    kind: string;
    key: string;
    label: string;
    holders: string[];
    /** 基准端（配置端优先 / 判定候选首端回退）；confirm 行无基准留空。 */
    baselineFork?: string;
  }

  async function loadInbox(): Promise<void> {
    try {
      const [conflicts, beatmaps] = await Promise.all([
        CompareService.listConflicts(),
        CompareService.library("beatmap_set" as EntityKind),
      ]);
      const items: InboxItem[] = conflicts.map((c) => ({
        mode: "conflict",
        kind: c.Kind,
        key: c.ContentKey,
        label: c.Label || c.ContentKey,
        holders: [...c.Forks].sort(),
        baselineFork: c.BaselineFork || undefined,
      }));
      for (const it of beatmaps) {
        if (it.State !== "confirm") continue;
        items.push({
          mode: "confirm",
          kind: "beatmap_set",
          key: it.ContentKey,
          label: it.Summary || it.ContentKey,
          holders: it.Forks.filter((f) => f.State === "confirm").map((f) => f.ForkID),
        });
      }
      paintInbox(items);
    } catch (e) {
      toast("err", errText(e));
    }
  }

  function paintInbox(items: InboxItem[]): void {
    clear(inboxBox);
    if (items.length === 0) {
      inboxBox.classList.add("hidden");
      return;
    }
    inboxBox.classList.remove("hidden");
    inboxBox.append(el("div", { class: "ib-title" }, [text(`${t("lib.pending")} ${items.length}`)]));
    for (const item of items) inboxBox.append(inboxRow(item));
  }

  /** orderedHolders 持有端排序：基准端排最前，其余保持字母序。 */
  function orderedHolders(item: InboxItem): string[] {
    if (!item.baselineFork) return item.holders;
    return [item.baselineFork, ...item.holders.filter((f) => f !== item.baselineFork)];
  }

  function inboxRow(item: InboxItem): HTMLElement {
    const row = el("div", { class: `irow ${item.mode}` });
    const check = el("input", { class: "icheck", type: "checkbox" }) as HTMLInputElement;
    const holdersEl = el("span", { class: "iholders" });
    row.append(
      el("span", { class: "ibar" }),
      check,
      el("span", { class: "ikind" }, [text(kindLabel(item.kind))]),
      el("span", { class: "ilabel" }, [text(item.label)]),
      holdersEl,
      el("span", { class: "ichoice" }),
      el("span", { class: "ichev" }, [icon("chevron")]),
    );
    const choice = row.querySelector<HTMLElement>(".ichoice")!;
    check.addEventListener("click", (e) => e.stopPropagation()); // 不触发展开

    // 外环：staged winner 实环（已拍板）；未拍板时配置基准端弱化预览。
    const stagedEntry = () => staged.get(item.kind, item.key);
    const stagedWinner = (): string | undefined => {
      const e = stagedEntry();
      return e?.action === "winner" ? e.winner : undefined;
    };
    function paintHolders(): void {
      clear(holdersEl);
      const marked = stagedWinner() ?? item.baselineFork;
      const markedByUser = stagedWinner() !== undefined;
      for (const f of orderedHolders(item)) {
        const d = forkDot(f, 10);
        if (f === marked) {
          d.classList.add("base");
          if (!markedByUser) d.classList.add("dim"); // 默认基准弱化预览
          d.title = `${f} · ${t("inbox.baseline")}`;
        }
        holdersEl.append(d);
      }
    }
    function syncCheck(): void {
      check.checked = stagedEntry() !== undefined;
    }
    paintHolders();
    syncCheck();

    // 勾选 = 加入暂存（已选 winner 则 winner，否则 select=默认基准）；
    // 取消 = 摘出暂存。
    check.addEventListener("change", () => {
      if (check.checked) {
        const w = stagedWinner();
        staged.put(w !== undefined
          ? { kind: item.kind, contentKey: item.key, label: item.label, action: "winner", winner: w }
          : { kind: item.kind, contentKey: item.key, label: item.label, action: "select", winner: "" });
      } else {
        staged.remove(item.kind, item.key);
      }
      const e = stagedEntry();
      paintChoice(choice, e?.action === "winner" ? e.winner : e?.action);
      paintHolders();
    });

    const detail = el("div", { class: "idetail hidden" });
    row.append(detail);
    let loaded = false;
    row.addEventListener("click", () => {
      const open = detail.classList.toggle("hidden");
      row.classList.toggle("open", !open);
      if (loaded || open) return;
      loaded = true;
      if (item.mode === "confirm") {
        detail.append(singleCard(t("decide.update"), (a) => pick(a)));
      } else {
        detail.append(el("div", { class: "vdiffs" }, [text("…")]));
        CompareService.unitConflict(item.kind as EntityKind, item.key)
          .then((cd) => {
            clear(detail);
            detail.append(conflictCards(cd, (a) => pick(a), stagedWinner(), item.baselineFork));
          })
          .catch((e) => {
            clear(detail);
            toast("err", errText(e));
          });
      }
    });

    function pick(a: import("./decide").DecideAction): void {
      if (a.action === "keep") {
        staged.put({ kind: item.kind, contentKey: item.key, label: item.label, action: "keep", winner: "" });
      } else if (a.action === "winner") {
        staged.put({ kind: item.kind, contentKey: item.key, label: item.label, action: "winner", winner: a.winner });
      } else {
        staged.put({ kind: item.kind, contentKey: item.key, label: item.label, action: "select", winner: "" });
      }
      const e = stagedEntry();
      paintChoice(choice, e?.action === "winner" ? e.winner : e?.action);
      paintHolders();
      syncCheck(); // 点卡自动勾
      // 就地更新卡选中态（不重建，.sel 跟随最新 staged winner）
      const w = stagedWinner();
      detail.querySelectorAll<HTMLElement>(".vcard").forEach((c) => {
        const f = c.dataset.fork;
        c.classList.toggle("sel", f !== undefined && f === w);
        c.classList.toggle("pre", f !== undefined && w === undefined && f === item.baselineFork);
      });
      detail.classList.add("hidden");
      row.classList.remove("open");
    }
    const e0 = stagedEntry();
    paintChoice(choice, e0?.action === "winner" ? e0.winner : e0?.action);
    return row;
  }

  function paintChoice(node: HTMLElement, sel: string | undefined): void {
    clear(node);
    if (!sel || sel === "select") return; // 外环已标记选中端，select/未选不显示
    if (sel === "keep") {
      node.append(text("="));
    }
    // winner 不画重复点——holders 外环已标。
  }

  // ---------- 订阅与清理 ----------

  unsubs.push(onRun(repaintRun));
  unsubs.push(onData(() => void loadAll()));
  // 拖拽高亮「＋」（mygo 透传文件落到 onFileDrop；DOM 拖拽事件能触发则高亮）。
  const addEl = () => topo.querySelector<HTMLElement>(".client.add");
  const dragOn = () => addEl()?.classList.add("drag-hi");
  const dragOff = () => addEl()?.classList.remove("drag-hi");
  document.addEventListener("dragenter", dragOn);
  document.addEventListener("dragleave", dragOff);
  document.addEventListener("drop", dragOff);
  unsubs.push(() => {
    document.removeEventListener("dragenter", dragOn);
    document.removeEventListener("dragleave", dragOff);
    document.removeEventListener("drop", dragOff);
    closePopover();
  });

  void loadAll();
  return () => unsubs.forEach((u) => u());
}

/** registerPaths 供 main.ts 的 onFileDrop 调用。 */
export async function registerPaths(paths: string[]): Promise<void> {
  for (const path of paths) {
    try {
      const ref = await LibraryService.register(path);
      if (ref.ForkID === "") {
        addPendingClient({ realmPath: ref.RealmPath, diskVersion: ref.DiskVersion });
      } else {
        toast("ok", t("toast.registered", ref.ForkID));
      }
    } catch (e) {
      toast("err", errText(e));
    }
  }
  bumpData();
}
