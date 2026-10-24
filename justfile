# LZSync 开发常用指令（工具链与 webview 运行库来自 manifest.scm，经 direnv use guix 加载）
# bun run <script> 直接透传 package.json scripts。

# 列出全部配方
default:
    @just --list

# Go 构建与静态检查
go-check:
    go build ./... && go vet ./...

# Go 全量测试（含 itests 集成断言）
test:
    go test ./... -count=1

# 前端类型检查（tsc strict + noUncheckedIndexedAccess）
typecheck:
    bun run typecheck

# mygo generate：改 appsvc Bind 服务面后重新生成 src/mygo.ts 绑定
generate:
    bun run generate

# mygo build 完整打包：tsc + vite build + go build → build/linux-amd64/
package:
    bun run build

# 四道门禁一次跑全（等价 AGENTS.md 的全量验收）
gates: go-check test typecheck package

# webview 运行库路径：guix 无全局库路径，取 manifest 环境 profile 的 lib
#（direnv use guix 设 GUIX_ENVIRONMENT；未进 direnv 环境时启动会报缺 libglib）
webview-libs := "$GUIX_ENVIRONMENT/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"

# 打包并启动主程序（调试入口：窗口打开即最新构建）
run: package
    LD_LIBRARY_PATH={{webview-libs}} ./build/linux-amd64/lzsync

# mygo dev 热重载开发模式（vite dev server + 5173，窗口同步需要 webview 库）
dev:
    LD_LIBRARY_PATH={{webview-libs}} bun run dev
