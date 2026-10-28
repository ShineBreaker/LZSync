# lzsync.spec — LZSync 的 RPM 打包定义（Fedora/RHEL 系）。
# 由 scripts/package-rpm.sh 驱动：payload 目录已按安装布局装配在
# %{lzsync_payload}（默认 %{_builddir}/lzsync-payload），本 spec 只做
# 复制与声明。版本经 rpmbuild --define "lzsync_version X.Y.Z" 注入。
#
# 布局与 mygo deb 对齐：应用在 /opt/lzsync/（可执行文件 + realm-core
# 共享库 + 资源同目录，realmc 打包路径解析命中），/usr/bin 建链接，
# desktop 入口与 hicolor 图标进 share/。

%global lzsync_version 0.0.0
%global lzsync_release 1%{?dist}
%global lzsync_payload %{_builddir}/lzsync-payload

Name:           lzsync
Version:        %{lzsync_version}
Release:        %{lzsync_release}
Summary:        同步多个 osu!lazer fork 的数据目录
License:        MIT
URL:            https://github.com/ShineBreaker/LZSync
# mygo Linux 后端 Dlopen libgtk-3/libwebkit2gtk-4.1（4.0 亦可兜底，但
# Fedora 系现行为 webkit2gtk4.1 包名）
Requires:       gtk3
Requires:       webkit2gtk4.1

%description
LZSync 以 SQLite 中枢为单一真相源，对同一台机器上的多个 osu!lazer
fork（官方 lazer、DeltaLazer、Ez2Lazer、g0v0）执行收集（fork → hub）
与投放（hub → fork）双向同步：曲库、收藏夹、皮肤、成绩，实体文件入
自有内容寻址仓、游戏侧以链接方式持有。

%prep
# payload 已由 scripts/package-rpm.sh 装配，无源码包需要解包

%build
# 预编译产物（Go 编译在 rpmbuild 之前完成），无构建步骤

%install
rm -rf %{buildroot}
mkdir -p %{buildroot}
cp -a %{lzsync_payload}/. %{buildroot}/

%post
command -v update-desktop-database >/dev/null && update-desktop-database -q /usr/share/applications || :
command -v gtk-update-icon-cache >/dev/null && gtk-update-icon-cache -q -t -f /usr/share/icons/hicolor || :

%postun
command -v update-desktop-database >/dev/null && update-desktop-database -q /usr/share/applications || :
command -v gtk-update-icon-cache >/dev/null && gtk-update-icon-cache -q -t -f /usr/share/icons/hicolor || :

%files
/opt/lzsync/
/usr/bin/lzsync
/usr/share/applications/lzsync.desktop
/usr/share/icons/hicolor/1024x1024/apps/lzsync.png

%changelog
* Tue Oct 28 2026 ShineBreaker - 1.0.0-1
- 首个发布版本
