;; LZSync guix 打包定义：`guix build -f lzsync.scm`。
;;
;; 打包对象是 `just package`（mygo build）的产物——编译本身仍由 Go/bun 完成；
;; 本包负责把产物装入 store 并 wrap 出**独立可运行**环境：
;;   - 捆绑 build/native 的 librealm-ffi.so 并以 LZSYNC_LIBREALM 指向它；
;;   - webview 运行库（guix 无全局库路径，wrap 补 LD_LIBRARY_PATH 等搜索路径）；
;;   - desktop 入口改写 Exec 为绝对路径、安装 hicolor 图标。
;; 先跑 `just package` 再 guix build；产物源目录缺失即构建失败（fail fast）。
;; 说明：依赖源码的 hermetic 构建（Go/npm 全量 vendoring）成本百 MB 级，暂不采用。
;;
;; version 与 mygo.config.ts 保持一致；升版本时两处同改。
(use-modules (guix packages)
             (guix gexp)
             (guix licenses)
             (guix build-system trivial)
             (gnu packages bash)
             (gnu packages glib)
             (gnu packages gnome)
             (gnu packages gtk)
             (gnu packages webkit))

(define dist-dir
  ;; 整目录入 store（含 deb/tar.gz 共 ~12MB 冗余——local-file 的 #:select?
  ;; 在当前 guix 上筛出空集，行为存疑，弃用换取确定性）。
  (local-file "build/linux-amd64" "lzsync-dist" #:recursive? #t))

(define librealm-so
  (local-file "build/native/linux-amd64/librealm-ffi.so" "librealm-ffi.so"))

(package
  (name "lzsync")
  (version "1.0.0")
  (source #f)
  (build-system trivial-build-system)
  (synopsis "管理多个 osu!lazer fork 数据目录的同步工具")
  (description
   "LZSync 以 SQLite 中枢为单一真相源，对同一台机器上的多个 osu!lazer
fork（官方 lazer、DeltaLazer、Ez2Lazer、g0v0）执行收集（fork→hub）与
投放（hub→fork）双向同步：曲库、收藏夹、皮肤、成绩，实体文件入自有
内容寻址仓、游戏侧以链接方式持有。")
  (home-page #f)
  (license expat)
  (inputs (list bash-minimal
                glib
                glib-networking
                gtk+
                webkitgtk-for-gtk3
                libsecret
                gsettings-desktop-schemas))
  (arguments
   (list
    #:modules '((guix build utils))
    #:builder
    #~(begin
        (use-modules (guix build utils))
        (let* ((out #$output)
               (bin-dir (string-append out "/bin"))
               (lib-dir (string-append out "/lib/lzsync"))
               (share (string-append out "/share"))
               (bin (string-append bin-dir "/lzsync")))
          (mkdir-p bin-dir)
          (mkdir-p lib-dir)
          (mkdir-p (string-append share "/applications"))
          (mkdir-p (string-append share "/icons/hicolor/1024x1024/apps"))
          (copy-file (string-append #$dist-dir "/lzsync") bin)
          (chmod bin #o555)
          (copy-file #$librealm-so
                     (string-append lib-dir "/librealm-ffi.so"))
          (copy-file (string-append #$dist-dir "/lzsync.png")
                     (string-append share "/icons/hicolor/1024x1024/apps/lzsync.png"))
          (let ((desktop (string-append share "/applications/lzsync.desktop")))
            (copy-file (string-append #$dist-dir "/lzsync.desktop") desktop)
            (substitute* desktop
              (("Exec=lzsync") (string-append "Exec=" bin))))
          ;; wrap-program（guix build utils）为二进制生成 env 包壳；
          ;; trivial builder 不自动把 inputs 放进 PATH，先补 bash 供其写 shebang。
          (setenv "PATH" (string-append #$bash-minimal "/bin:" (or (getenv "PATH") "")))
          (wrap-program bin
            `("LZSYNC_LIBREALM" ":" = (,(string-append lib-dir "/librealm-ffi.so")))
            `("LD_LIBRARY_PATH" ":" prefix
              ,(cons lib-dir
                     (search-path-as-list '("lib")
                                          (list #$glib #$gtk+ #$webkitgtk-for-gtk3 #$libsecret))))
            `("XDG_DATA_DIRS" ":" prefix
              ,(search-path-as-list '("share")
                                    (list #$glib #$gtk+ #$gsettings-desktop-schemas)))
            `("GIO_EXTRA_MODULES" ":" prefix
              ,(search-path-as-list '("lib/gio/modules") (list #$glib-networking)))
            `("GUIX_GTK3_PATH" ":" prefix
              ,(search-path-as-list '("lib/gtk-3.0") (list #$gtk+))))
          #t)))))
