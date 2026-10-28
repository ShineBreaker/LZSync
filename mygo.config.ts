import { defineConfig } from "mygo-cli";

export default defineConfig({
  name: "LZSync",
  identifier: "io.github.shinebreaker.lzsync",
  version: "1.0.0",
  copyright: "Copyright © 2026 ShineBreaker",
  // `mygo dev` runs devCommand and loads devUrl; `mygo build` runs
  // buildCommand and embeds frontendDist into the app.
  devUrl: "http://localhost:5173",
  // bun 已装进 guix home profile（系统 PATH，1.4.4），mygo 子进程裸调可见；
  // 早先 ~/.npm-global 时代的 npm 回退已废弃（npm 全局包已删除）。
  devCommand: "bun run dev:web",
  buildCommand: "bun run build:web",
  frontendDist: "dist",
  bindings: "src/mygo.ts",
  out: "build",
  linux: {
    comment: "同步多个 osu!lazer fork 的数据：曲库、收藏夹、皮肤、成绩",
    categories: ["Utility", "Game"],
    // deb 在 /usr/bin 建 lzsync 链接、install.sh 在 ~/.local/bin 建链接
    command: "lzsync",
    maintainer: "ShineBreaker",
  },
  macos: {
    dmgTitle: "LZSync",
  },
});
