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

# mygo build 完整编译打包：tsc + vite build + go build → build/linux-amd64/
build:
    bun run build

# guix 打包（先 build）：装配进 store 并 wrap 出独立可运行环境
package: build
    guix build -f lzsync.scm

# 四道门禁一次跑全（等价 AGENTS.md 的全量验收）
gates: go-check test typecheck build

# 启动主程序（调试入口）：优先跑 guix 包的 wrapped binary（自带运行环境），
# guix 不可用/打包失败时回退直跑 build 产物（经 manifest 环境补 webview 库）。
# webview 运行库经 guix shell 供给——direnv 的 use guix 只导出 search-paths
# 不设 GUIX_ENVIRONMENT，且 just 的 recipe 以 sh -eu 运行、unset 即报错。
run: build
    out=$(guix build -f lzsync.scm 2>/dev/null) && exec "$out/bin/lzsync" || guix shell -m manifest.scm -- sh -c 'LD_LIBRARY_PATH="$GUIX_ENVIRONMENT/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}" ./build/linux-amd64/lzsync'

# mygo dev 热重载开发模式（vite dev server + 5173，窗口同步需要 webview 库）
dev:
    guix shell -m manifest.scm -- sh -c 'LD_LIBRARY_PATH="$GUIX_ENVIRONMENT/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}" bun run dev'
