package inisettings

// game.ini 白名单键策略（design.md §3.1 / §5.1-M3；fork-comparison D12/
// D14/D15）。分层口径：
//   - 共有集 sharedKeys：官方 OsuSetting 122 键全量（lazer-baseline §4.2
//     摘录，C-1 自包含）− delta 缺的 3 键（D12：SongSelectCollectionFilter
//     /HitsoundVolume/PMFriendsOnly；HitsoundVolume 另有归属记录冲突 U16，
//     两说都指向「非四端共有」，保守不入）= 119 键。这是四端都认识的
//     结构事实集，含红灯键（Token/Username/ReleaseStream 在枚举内）；
//   - 红灯 Locked / 专有 Own 是 per-fork 策略数据，数据化于 forks 注册表
//     的 ForkProfile.IniKeys（本包不内置任何具体 fork 名单）；四端一致
//     的红灯 = Token/Username（OAuth 凭据，D15）与 ReleaseStream（每端
//     身份配置，D14）——见 forks 包 lockedIniKeys；
//   - 同步面 = 共有集 − 两端 Locked − 两端 Own。红灯键在任何组合下
//     永不进入同步面（S10：断言其不可被勾选的判定数据即 IsRedLight）。

import "lzsync/internal/forks"

// sharedKeys 四端共有键（官方 OsuSetting 枚举 ∩ 四端，119 项；来源与
// 剔除依据见包注释）。键名与源码枚举成员逐字一致（Ordinal）。
var sharedKeys = map[string]bool{
	// …以下按 OsuConfigManager.cs 源码顺序全量固化…
	"Ruleset": true, "Token": true, "MenuCursorSize": true, "GameplayCursorSize": true,
	"AutoCursorSize": true, "GameplayCursorDuringTouch": true,
	"DimLevel": true, "BlurLevel": true, "EditorDim": true, "LightenDuringBreaks": true,
	"ShowStoryboard": true, "KeyOverlay": true,
	"GameplayLeaderboard": true, "PositionalHitsoundsLevel": true, "AlwaysPlayFirstComboBreak": true,
	"FloatingComments": true,
	"HUDVisibilityMode": true, "ShowHealthDisplayWhenCantFail": true, "FadePlayfieldWhenHealthLow": true,
	"MouseDisableButtons": true, "MouseDisableWheel": true, "ConfineMouseMode": true,
	"AudioOffset": true, "VolumeInactive": true,
	"MenuMusic": true, "MenuVoice": true, "MenuTips": true, "CursorRotation": true,
	"MenuParallax": true, "MenuParallaxScale": true,
	"Prefer24HourTime": true, "BeatmapDetailTab": true, "BeatmapLeaderboardSortMode": true,
	"BeatmapDetailModsFilter": true,
	"Username": true, "ReleaseStream": true, "SavePassword": true, "SaveUsername": true,
	"DisplayStarsMinimum": true, "DisplayStarsMaximum": true,
	"SongSelectGroupMode": true, "SongSelectSortingMode": true, "RandomSelectAlgorithm": true,
	"ModSelectHotkeyStyle": true, "ShowFpsDisplay": true, "ChatDisplayHeight": true,
	"BeatmapListingCardSize": true,
	"ToolbarClockDisplayMode": true, "SongSelectBackgroundBlur": true, "Version": true,
	"ShowFirstRunSetup": true,
	"ShowConvertedBeatmaps": true, "Skin": true, "ScreenshotFormat": true,
	"ScreenshotCaptureMenuCursor": true, "BeatmapSkins": true,
	"BeatmapColours": true, "BeatmapHitsounds": true, "IncreaseFirstObjectVisibility": true,
	"ScoreDisplayMode": true,
	"ExternalLinkWarning": true, "PreferNoVideo": true, "Scaling": true,
	"ScalingPositionX": true, "ScalingPositionY": true, "ScalingSizeX": true,
	"ScalingSizeY": true, "ScalingBackgroundDim": true, "UIScale": true, "IntroSequence": true,
	"NotifyOnUsernameMentioned": true,
	"NotifyOnPrivateMessage": true, "NotifyOnFriendPresenceChange": true,
	"UIHoldActivationDelay": true, "HitLighting": true,
	"StarFountains": true, "MenuBackgroundSource": true, "GameplayDisableWinKey": true,
	"SeasonalBackgroundMode": true,
	"EditorWaveformOpacity": true, "EditorShowHitMarkers": true, "EditorAutoSeekOnPlacement": true,
	"DiscordRichPresence": true,
	"ShowOnlineExplicitContent": true, "LastProcessedMetadataId": true, "SafeAreaConsiderations": true,
	"ComboColourNormalisationAmount": true, "ProfileCoverExpanded": true, "EditorLimitedDistanceSnap": true,
	"ReplaySettingsOverlay": true, "ReplayPlaybackControlsExpanded": true,
	"AutomaticallyDownloadMissingBeatmaps": true,
	"EditorShowSpeedChanges": true, "TouchDisableGameplayTaps": true, "ModSelectTextSearchStartsActive": true,
	"UserOnlineStatus": true, "MultiplayerRoomFilter": true, "HideCountryFlags": true,
	"EditorTimelineShowTimingChanges": true,
	"EditorTimelineShowTicks": true, "AlwaysShowHoldForMenuButton": true, "EditorContractSidebars": true,
	"EditorScaleOrigin": true,
	"EditorRotationOrigin": true, "EditorTimelineShowBreaks": true,
	"EditorAdjustExistingObjectsOnTimingChanges": true,
	"AlwaysRequireHoldingForPause": true, "MultiplayerShowInProgressFilter": true,
	"MultiplayerShowFullFilter": true,
	"BeatmapListingFeaturedArtistFilter": true, "ShowMobileDisclaimer": true, "EditorShowStoryboard": true,
	"EditorSubmissionNotifyOnDiscussionReplies": true, "EditorSubmissionLoadInBrowserAfterSubmission": true,
	"WasSupporter": true, "LastOnlineTagsPopulation": true, "AutomaticallyAdjustBeatmapOffset": true,
	"DashboardSortMode": true,
	"DashboardDisplayStyle": true,
}

// SharedKeys 返回四端共有键集合的拷贝（快照口径：含红灯键的结构事实集；
// 同步面请用 CollectableKeys/SyncableKeys）。
func SharedKeys() map[string]bool {
	out := make(map[string]bool, len(sharedKeys))
	for k := range sharedKeys {
		out[k] = true
	}
	return out
}

// IsShared 键是否属于四端共有集。
func IsShared(key string) bool { return sharedKeys[key] }

// IsRedLight 键是否命中端红灯名单（S10：Token/Username 凭据、ReleaseStream
// 身份配置等——数据来自 ForkProfile.IniKeys.Locked，UI 勾选判定的数据面，
// 断言「红灯键不可被勾选」即断言本判定覆盖全部同步入口）。
func IsRedLight(p forks.ForkProfile, key string) bool {
	for _, k := range p.IniKeys.Locked {
		if k == key {
			return true
		}
	}
	return false
}

// isOwn 键是否属于端专有名单（Own）。
func isOwn(p forks.ForkProfile, key string) bool {
	for _, k := range p.IniKeys.Own {
		if k == key {
			return true
		}
	}
	return false
}

// CollectableKeys 源端收集面：四端共有键 − 源端红灯键 − 源端专有键。
// 未注册 fork 的零值 Profile 使排除自然为空（引擎不内置 fork 知识）。
func CollectableKeys(p forks.ForkProfile) map[string]bool {
	out := make(map[string]bool, len(sharedKeys))
	for k := range sharedKeys {
		if IsRedLight(p, k) || isOwn(p, k) {
			continue
		}
		out[k] = true
	}
	return out
}

// SyncableKeys 源→目标同步面：四端共有键 − 两端红灯 − 两端专有。
// 红灯键在任何端组合下都不出现（S10 硬锁）。
func SyncableKeys(source, target forks.ForkProfile) map[string]bool {
	out := CollectableKeys(source)
	for k := range out {
		if IsRedLight(target, k) || isOwn(target, k) {
			delete(out, k)
		}
	}
	return out
}
