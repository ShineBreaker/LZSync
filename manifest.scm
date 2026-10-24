;; LZSync 开发环境（direnv use guix 自动加载；工具链与运行库全部在此声明）。
;; - go / bun / just：日常工具链（bun 由 guix 供给，勿再依赖 ~/.npm-global）。
;; - webkitgtk-for-gtk3（libwebkit2gtk-4.1）+ glib + gtk+ + libsecret：mygo
;;   webview 运行库——guix 无全局库路径，启动时由 justfile 以
;;   GUIX_ENVIRONMENT/lib 补 LD_LIBRARY_PATH。
;; - librealm（build/native/）日常直接取钉死产物；仅重建 realm-core 时才需
;;   scripts/fetch-librealm.sh 头注里的 gcc-toolchain@13 等重型工具，不进日常环境。
(specifications->manifest
 (list "go" "bun" "just"
       "webkitgtk-for-gtk3" "glib" "gtk+" "libsecret"))
