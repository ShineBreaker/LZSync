package deploy

// 已损坏的私服谱面库修复。
//
// 旧版本把 g0v0 的谱面 OnlineID 写成 -1，版本替换还会软删掉原本能开
// 排行榜的行，新行只剩官方的 Pending/Graveyard。这些差异不进同步计划
// （内容哈希没变），所以下一次同步要单独扫一遍。
//
// 能补的：
//   - 谱面 OnlineID：同一 BeatmapServerRealm 的 hub 记录（canonical 或
//     presence），或本库里同一张图的其他行（含已软删的旧版本）。
//   - 排行榜状态：只从本库旧行恢复。不从官方端抄 Ranked/Approved，
//     避免给目标服务器从未上过榜的图凭空开排行榜。
//
// 不碰成绩 id、用户 id，也不把已有的正数谱面 id 改成另一个数。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"lzsync/internal/forks"
	"lzsync/internal/hub"
	"lzsync/internal/realmc"
)

// BeatmapIdentityRepairReason 记入投放报告，说明这次补回了谱面排行榜身份。
const BeatmapIdentityRepairReason = "已修复谱面排行榜身份（补回缺失的谱面 OnlineID，或从本库旧行恢复排行榜状态）"

type beatmapSnap struct {
	hash          string
	deletePending bool
	onlineID      int64
	status        int64
	diffs         []diffSnap
}

type diffSnap struct {
	hash   string
	md5    string
	name   string
	online int64
	status int64
}

type idEvidence struct {
	setID      int64
	diffByHash map[string]int64
	diffByMD5  map[string]int64
}

type beatmapRepair struct {
	hash           string
	setID          int64
	setStatus      int64
	writeSetStatus bool
	diffs          []diffRepair
}

type diffRepair struct {
	hash        string
	onlineID    int64
	status      int64
	writeStatus bool
}

// RepairBeatmapLeaderboards 扫一遍目标库，把已经损坏的谱面排行榜身份
// 补回来。没有需要修的行时不备份、不写库。stable 没有 realm 谱面表，
// 直接返回 0。
func (r *Runner) RepairBeatmapLeaderboards(ctx context.Context, target forks.LibraryRef) (int, error) {
	profile, ok := forks.Profile(target.ForkID)
	if !ok || profile.Storage != forks.StorageRealm || target.RealmPath == "" {
		return 0, nil
	}
	if r.deps.Hub == nil {
		return 0, fmt.Errorf("deploy: Deps.Hub 未配置")
	}
	if r.deps.Guard != nil {
		if err := r.deps.Guard.EnsureClearFork(target.ForkID); err != nil {
			return 0, err
		}
	}
	if err := probeOccupied(target.RealmPath); err != nil {
		return 0, err
	}
	lib, err := realmc.Load()
	if err != nil {
		return 0, fmt.Errorf("deploy: native 库不可用: %w", err)
	}
	ro, err := lib.OpenDynamicReadOnly(target.RealmPath)
	if err != nil {
		return 0, fmt.Errorf("deploy: 打开目标库 %s: %w", target.RealmPath, err)
	}
	snaps, err := readBeatmapSnaps(ro)
	ro.Close()
	if err != nil {
		return 0, err
	}
	reps, err := r.planBeatmapRepairs(ctx, target.ForkID, snaps)
	if err != nil {
		return 0, err
	}
	if len(reps) == 0 {
		return 0, nil
	}
	if _, err := backupTarget(r.deps.Backups, target.RealmPath); err != nil {
		return 0, err
	}
	realm, err := lib.OpenDynamicReadWrite(target.RealmPath)
	if err != nil {
		return 0, fmt.Errorf("deploy: 打开目标库 %s: %w", target.RealmPath, err)
	}
	defer realm.Close()
	n, err := r.repairBeatmapLeaderboards(ctx, realm, target.ForkID)
	if err != nil {
		return 0, err
	}
	if n > 0 {
		if err := touchAfterWrite(target.RealmPath); err != nil {
			return n, err
		}
	}
	return n, nil
}

func readBeatmapSnaps(realm *realmc.Realm) ([]beatmapSnap, error) {
	if err := realm.BeginRead(); err != nil {
		return nil, err
	}
	w, err := prepareBeatmapSetWriter(realm)
	if err != nil || w == nil || w.absent {
		return nil, err
	}
	return w.snapshotBeatmaps()
}

func (r *Runner) repairBeatmapLeaderboards(ctx context.Context, realm *realmc.Realm, targetFork string) (int, error) {
	dg, err := beginDriftGuard(realm)
	if err != nil {
		return 0, err
	}
	defer dg.rollback()
	w, err := prepareBeatmapSetWriter(realm)
	if err != nil {
		return 0, err
	}
	if w.absent {
		if err := dg.finish(); err != nil {
			return 0, err
		}
		return 0, nil
	}
	snaps, err := w.snapshotBeatmaps()
	if err != nil {
		return 0, err
	}
	reps, err := r.planBeatmapRepairs(ctx, targetFork, snaps)
	if err != nil {
		return 0, err
	}
	n, err := w.applyBeatmapRepairs(reps)
	if err != nil {
		return 0, err
	}
	if err := dg.finish(); err != nil {
		return 0, err
	}
	return n, nil
}

func (w *beatmapSetWriter) snapshotBeatmaps() ([]beatmapSnap, error) {
	var out []beatmapSnap
	err := w.r.ForEach(w.setCK, func(obj realmObjectPtr) error {
		snap, err := w.readBeatmapSnap(obj)
		if err != nil {
			return err
		}
		if snap.hash != "" {
			out = append(out, snap)
		}
		return nil
	})
	return out, err
}

func (w *beatmapSetWriter) readBeatmapSnap(obj realmObjectPtr) (beatmapSnap, error) {
	var snap beatmapSnap
	var err error
	if key, ok := w.setCols["Hash"]; ok {
		snap.hash, err = w.r.GetString(obj, key)
		if err != nil {
			return snap, err
		}
	}
	if key, ok := w.setCols["DeletePending"]; ok {
		snap.deletePending, err = w.r.GetBool(obj, key)
		if err != nil {
			return snap, err
		}
	}
	if key, ok := w.setCols["OnlineID"]; ok {
		snap.onlineID, err = w.r.GetInt64(obj, key)
		if err != nil {
			return snap, err
		}
	}
	if key, ok := w.setCols["Status"]; ok {
		snap.status, err = w.r.GetInt64(obj, key)
		if err != nil {
			return snap, err
		}
	}
	lstKey, ok := w.setCols["Beatmaps"]
	if !ok {
		return snap, nil
	}
	lst, err := w.r.GetList(obj, lstKey)
	if err != nil {
		return snap, err
	}
	defer lst.Close()
	n, err := lst.Size()
	if err != nil {
		return snap, err
	}
	for i := uint64(0); i < n; i++ {
		bm, err := lst.GetLinkedObject(i)
		if err != nil {
			return snap, err
		}
		if bm == nil {
			continue
		}
		d, err := w.readDiffSnap(bm)
		w.r.ReleaseObject(bm)
		if err != nil {
			return snap, err
		}
		snap.diffs = append(snap.diffs, d)
	}
	return snap, nil
}

func (w *beatmapSetWriter) readDiffSnap(obj realmObjectPtr) (diffSnap, error) {
	var d diffSnap
	var err error
	if key, ok := w.bmCols["Hash"]; ok {
		d.hash, err = w.r.GetString(obj, key)
		if err != nil {
			return d, err
		}
	}
	if key, ok := w.bmCols["MD5Hash"]; ok {
		d.md5, err = w.r.GetString(obj, key)
		if err != nil {
			return d, err
		}
	}
	if key, ok := w.bmCols["DifficultyName"]; ok {
		d.name, err = w.r.GetString(obj, key)
		if err != nil {
			return d, err
		}
	}
	if key, ok := w.bmCols["OnlineID"]; ok {
		d.online, err = w.r.GetInt64(obj, key)
		if err != nil {
			return d, err
		}
	}
	if key, ok := w.bmCols["Status"]; ok {
		d.status, err = w.r.GetInt64(obj, key)
		if err != nil {
			return d, err
		}
	}
	return d, nil
}

func (r *Runner) planBeatmapRepairs(ctx context.Context, targetFork string, snaps []beatmapSnap) ([]beatmapRepair, error) {
	byHash := map[string]beatmapSnap{}
	for _, s := range snaps {
		byHash[s.hash] = s
	}
	var reps []beatmapRepair
	for _, s := range snaps {
		if s.deletePending || s.hash == "" || !snapNeedsRepair(s) {
			continue
		}
		preds, err := r.predecessorHashes(ctx, s.hash)
		if err != nil {
			return nil, err
		}
		hubEv, err := r.hubBeatmapIDs(ctx, targetFork, s.hash)
		if err != nil {
			return nil, err
		}
		if rep, ok := planOneRepair(s, preds, byHash, hubEv); ok {
			reps = append(reps, rep)
		}
	}
	return reps, nil
}

func snapNeedsRepair(s beatmapSnap) bool {
	if s.onlineID <= 0 || !showsLeaderboard(s.status) {
		return true
	}
	for _, d := range s.diffs {
		if d.online <= 0 || !showsLeaderboard(d.status) {
			return true
		}
	}
	return false
}

func planOneRepair(s beatmapSnap, preds []string, byHash map[string]beatmapSnap, hubEv idEvidence) (beatmapRepair, bool) {
	predsDonors, siblings := donorSnaps(s, preds, byHash)
	donors := append(append([]beatmapSnap{}, predsDonors...), siblings...)
	rep := beatmapRepair{hash: s.hash}
	changed := false
	if s.onlineID <= 0 {
		if id := chooseID(localSetIDs(donors), hubEv.setID); id > 0 {
			rep.setID = id
			changed = true
		}
	}
	if !showsLeaderboard(s.status) {
		if st, ok := chooseStatus(localSetStatuses(donors)); ok {
			rep.setStatus = st
			rep.writeSetStatus = true
			changed = true
		}
	}
	for _, d := range s.diffs {
		dr := diffRepair{hash: d.hash}
		diffChanged := false
		if d.online <= 0 && d.hash != "" {
			local := localDiffIDs(predsDonors, siblings, d)
			hub := hubEv.diffByHash[d.hash]
			if hub == 0 && d.md5 != "" {
				hub = hubEv.diffByMD5[d.md5]
			}
			if id := chooseID(local, hub); id > 0 {
				dr.onlineID = id
				diffChanged = true
			}
		}
		if !showsLeaderboard(d.status) {
			if st, ok := chooseStatus(localDiffStatuses(predsDonors, siblings, d)); ok {
				dr.status = st
				dr.writeStatus = true
				diffChanged = true
			}
		}
		if diffChanged && d.hash != "" {
			rep.diffs = append(rep.diffs, dr)
			changed = true
		}
	}
	return rep, changed
}

// donorSnaps 本库里可以当证据的其他行。preds 是版本链前驱（可以按
// 难度名配对，含软删）。siblings 只是哈希或 MD5 对得上的行，不能靠
// 难度名认亲。不包括自己。
func donorSnaps(s beatmapSnap, preds []string, byHash map[string]beatmapSnap) (predDonors, siblings []beatmapSnap) {
	seen := map[string]bool{s.hash: true}
	add := func(hash string, into *[]beatmapSnap) {
		if hash == "" || seen[hash] {
			return
		}
		other, ok := byHash[hash]
		if !ok {
			return
		}
		seen[hash] = true
		*into = append(*into, other)
	}
	for _, h := range preds {
		add(h, &predDonors)
	}
	for _, d := range s.diffs {
		for hash, other := range byHash {
			if seen[hash] {
				continue
			}
			for _, od := range other.diffs {
				if (d.hash != "" && od.hash == d.hash) || (d.md5 != "" && od.md5 == d.md5) {
					add(hash, &siblings)
					break
				}
			}
		}
	}
	return predDonors, siblings
}

func localSetIDs(donors []beatmapSnap) int64 {
	var cur int64
	for _, d := range donors {
		cur = mergeID(cur, d.onlineID)
	}
	return cur
}

func localSetStatuses(donors []beatmapSnap) int64 {
	var cur int64
	for _, d := range donors {
		if showsLeaderboard(d.status) {
			cur = mergeID(cur, d.status)
		}
	}
	return cur
}

func localDiffIDs(preds, siblings []beatmapSnap, d diffSnap) int64 {
	var cur int64
	for _, donor := range preds {
		if od, ok := matchDiff(donor, d, true); ok {
			cur = mergeID(cur, od.online)
		}
	}
	for _, donor := range siblings {
		if od, ok := matchDiff(donor, d, false); ok {
			cur = mergeID(cur, od.online)
		}
	}
	return cur
}

func localDiffStatuses(preds, siblings []beatmapSnap, d diffSnap) int64 {
	var cur int64
	for _, donor := range preds {
		if od, ok := matchDiff(donor, d, true); ok && showsLeaderboard(od.status) {
			cur = mergeID(cur, od.status)
		}
	}
	for _, donor := range siblings {
		if od, ok := matchDiff(donor, d, false); ok && showsLeaderboard(od.status) {
			cur = mergeID(cur, od.status)
		}
	}
	return cur
}

// matchDiff 在捐赠行里找对应难度。nameOK 为真时才允许按难度名匹配
// （只用于版本链前驱；随便一张同名难度不能当证据）。
func matchDiff(donor beatmapSnap, d diffSnap, nameOK bool) (diffSnap, bool) {
	for _, od := range donor.diffs {
		if d.hash != "" && od.hash == d.hash {
			return od, true
		}
	}
	for _, od := range donor.diffs {
		if d.md5 != "" && od.md5 == d.md5 {
			return od, true
		}
	}
	if nameOK && d.name != "" {
		for _, od := range donor.diffs {
			if od.name == d.name {
				return od, true
			}
		}
	}
	return diffSnap{}, false
}

// chooseID 本库证据优先。两边都没有、或同一边自己打架，就不动。
func chooseID(local, hub int64) int64 {
	if local > 0 {
		return local
	}
	if local < 0 {
		return 0
	}
	if hub > 0 {
		return hub
	}
	return 0
}

func chooseStatus(local int64) (int64, bool) {
	return local, local > 0
}

// mergeID 把另一个正数并进来。0 = 还没有，-1 = 冲突。
func mergeID(cur, v int64) int64 {
	if v <= 0 {
		return cur
	}
	if cur == 0 {
		return v
	}
	if cur != v {
		return -1
	}
	return cur
}

func (r *Runner) hubBeatmapIDs(ctx context.Context, targetFork, hash string) (idEvidence, error) {
	ev := idEvidence{diffByHash: map[string]int64{}, diffByMD5: map[string]int64{}}
	target, ok := forks.Profile(targetFork)
	if !ok || r.deps.Hub == nil || hash == "" {
		return ev, nil
	}
	want := target.BeatmapRealm()
	db := r.deps.Hub.DB()
	note := func(forkID, payload string) {
		p, ok := forks.Profile(forkID)
		if !ok || p.BeatmapRealm() != want {
			return
		}
		var bs hub.BeatmapSet
		if json.Unmarshal([]byte(payload), &bs) != nil {
			return
		}
		ev.setID = mergeID(ev.setID, bs.OnlineID)
		for _, bm := range bs.Beatmaps {
			if bm.Hash != "" {
				ev.diffByHash[bm.Hash] = mergeID(ev.diffByHash[bm.Hash], int64(bm.OnlineID))
			}
			if bm.MD5Hash != "" {
				ev.diffByMD5[bm.MD5Hash] = mergeID(ev.diffByMD5[bm.MD5Hash], int64(bm.OnlineID))
			}
		}
	}
	var origin, payload string
	err := db.QueryRowContext(ctx,
		`SELECT origin_fork, payload FROM beatmap_set WHERE content_hash=?`, hash).Scan(&origin, &payload)
	if err != nil && err != sql.ErrNoRows {
		return ev, fmt.Errorf("deploy: 读谱面集 %s: %w", hash, err)
	}
	if err == nil {
		note(origin, payload)
	}
	rows, err := db.QueryContext(ctx,
		`SELECT fork_id, local_values FROM presence WHERE kind=? AND content_key=?`,
		hub.KindBeatmapSet, hash)
	if err != nil {
		return ev, err
	}
	defer rows.Close()
	for rows.Next() {
		var forkID, local string
		if err := rows.Scan(&forkID, &local); err != nil {
			return ev, err
		}
		note(forkID, local)
	}
	return ev, rows.Err()
}

func (r *Runner) predecessorHashes(ctx context.Context, hash string) ([]string, error) {
	if r.deps.Hub == nil || hash == "" {
		return nil, nil
	}
	db := r.deps.Hub.DB()
	seen := map[string]bool{hash: true}
	frontier := []string{hash}
	var out []string
	for len(frontier) > 0 && len(out) < 32 {
		cur := frontier[0]
		frontier = frontier[1:]
		rows, err := db.QueryContext(ctx,
			`SELECT content_hash FROM beatmap_set WHERE superseded_by=?`, cur)
		if err != nil {
			return nil, err
		}
		var next []string
		for rows.Next() {
			var h string
			if err := rows.Scan(&h); err != nil {
				rows.Close()
				return nil, err
			}
			if h == "" || seen[h] {
				continue
			}
			seen[h] = true
			next = append(next, h)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		out = append(out, next...)
		frontier = append(frontier, next...)
	}
	return out, nil
}

func (w *beatmapSetWriter) applyBeatmapRepairs(reps []beatmapRepair) (int, error) {
	n := 0
	for _, rep := range reps {
		obj, err := w.r.FindFirstByString(w.setCK, w.setCols["Hash"], rep.hash)
		if err != nil {
			return n, err
		}
		if obj == nil {
			continue
		}
		wrote, err := w.applyOneRepair(obj, rep)
		w.r.ReleaseObject(obj)
		if err != nil {
			return n, err
		}
		if wrote {
			n++
		}
	}
	return n, nil
}

func (w *beatmapSetWriter) applyOneRepair(obj realmObjectPtr, rep beatmapRepair) (bool, error) {
	wrote := false
	if rep.setID > 0 {
		if key, ok := w.setCols["OnlineID"]; ok {
			cur, err := w.r.GetInt64(obj, key)
			if err != nil {
				return false, err
			}
			if cur <= 0 && cur != rep.setID {
				if err := w.r.SetInt64(obj, key, rep.setID); err != nil {
					return false, err
				}
				wrote = true
			}
		}
	}
	if rep.writeSetStatus && showsLeaderboard(rep.setStatus) {
		if key, ok := w.setCols["Status"]; ok {
			cur, err := w.r.GetInt64(obj, key)
			if err != nil {
				return false, err
			}
			if !showsLeaderboard(cur) {
				if err := w.r.SetInt64(obj, key, rep.setStatus); err != nil {
					return false, err
				}
				wrote = true
			}
		}
	}
	if len(rep.diffs) == 0 {
		return wrote, nil
	}
	lstKey, ok := w.setCols["Beatmaps"]
	if !ok {
		return wrote, nil
	}
	lst, err := w.r.GetList(obj, lstKey)
	if err != nil {
		return false, err
	}
	defer lst.Close()
	n, err := lst.Size()
	if err != nil {
		return false, err
	}
	byHash := map[string]diffRepair{}
	for _, d := range rep.diffs {
		byHash[d.hash] = d
	}
	hashKey, hasHash := w.bmCols["Hash"]
	for i := uint64(0); i < n; i++ {
		bm, err := lst.GetLinkedObject(i)
		if err != nil {
			return false, err
		}
		if bm == nil || !hasHash {
			if bm != nil {
				w.r.ReleaseObject(bm)
			}
			continue
		}
		h, err := w.r.GetString(bm, hashKey)
		if err != nil {
			w.r.ReleaseObject(bm)
			return false, err
		}
		d, ok := byHash[h]
		if !ok {
			w.r.ReleaseObject(bm)
			continue
		}
		dw, err := w.applyDiffRepair(bm, d)
		w.r.ReleaseObject(bm)
		if err != nil {
			return false, err
		}
		if dw {
			wrote = true
		}
	}
	return wrote, nil
}

func (w *beatmapSetWriter) applyDiffRepair(obj realmObjectPtr, d diffRepair) (bool, error) {
	wrote := false
	if d.onlineID > 0 {
		if key, ok := w.bmCols["OnlineID"]; ok {
			cur, err := w.r.GetInt64(obj, key)
			if err != nil {
				return false, err
			}
			if cur <= 0 {
				if err := w.r.SetInt64(obj, key, d.onlineID); err != nil {
					return false, err
				}
				wrote = true
			}
		}
	}
	if d.writeStatus && showsLeaderboard(d.status) {
		if key, ok := w.bmCols["Status"]; ok {
			cur, err := w.r.GetInt64(obj, key)
			if err != nil {
				return false, err
			}
			if !showsLeaderboard(cur) {
				if err := w.r.SetInt64(obj, key, d.status); err != nil {
					return false, err
				}
				wrote = true
			}
		}
	}
	return wrote, nil
}
