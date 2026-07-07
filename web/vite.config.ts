import {defineConfig} from 'vite'
import react from '@vitejs/plugin-react'

// https://vitejs.dev/config/
// Wails dev mode (vite serve) and prod mode (wails build -> vite build)
// disagree about how to emit asset paths:
//
//   * Production: the Wails window loads from the `wails://` scheme
//     and absolute `/assets/...` paths silently 404, leaving a black
//     webview. Emit relative `./assets/...` so paths resolve against
//     the wails:// origin.
//   * Development: wails dev proxies localhost:34115 -> vite at 5173,
//     and `base: './'` breaks both the HMR client injection
//     (`./@vite/client`) and module loading through the proxy. We
//     keep the default `/` for dev so HMR + module resolution work.
//
// The defineConfig callback form gives us a per-command `base`.
export default defineConfig(({ command }) => ({
  base: command === 'build' ? './' : '/',
  plugins: [react()]
}))
