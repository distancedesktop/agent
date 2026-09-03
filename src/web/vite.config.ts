import { defineConfig } from 'vite'

// The agent embeds `web/dist` (this directory's build output) via go:embed and
// serves it on :52022. We use relative base so assets resolve no matter the
// mount path, and we keep a stable, reviewable build (no framework).
export default defineConfig({
  base: './',
  build: {
    outDir: 'dist',
    target: 'es2022',
    sourcemap: false,
    assetsInlineLimit: 0,
    chunkSizeWarningLimit: 1024
  },
  server: {
    port: 52023,
    host: true
  }
})
