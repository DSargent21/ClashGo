import {createRoot} from 'react-dom/client'
import './style.css'
import './styles/design.css'
import './styles/views.css'
import App from './App'
import ErrorBoundary from './components/ErrorBoundary'
import interWoff2 from './assets/fonts/inter-latin-var.woff2?url'
import monoWoff2 from './assets/fonts/jetbrains-mono-latin-var.woff2?url'

/**
 * Preload both font faces before the first paint.
 *
 * The faces are hashed build assets, so their URLs only exist at bundle time —
 * injecting the <link> here is how a preload can point at them. `crossorigin`
 * is required on font preloads, otherwise the browser discards the preloaded
 * response and fetches the font a second time.
 */
const preloadFont = (href: string) => {
    const link = document.createElement('link')
    link.rel = 'preload'
    link.as = 'font'
    link.type = 'font/woff2'
    link.crossOrigin = 'anonymous'
    link.href = href
    document.head.appendChild(link)
}

preloadFont(interWoff2)
preloadFont(monoWoff2)

const container = document.getElementById('root')

const root = createRoot(container!)

// The ErrorBoundary wraps the whole tree so an uncaught render error
// renders a visible "reload" fallback instead of unmounting to the
// Wails transparent-webview black screen (see ErrorBoundary.tsx).
root.render(
    <ErrorBoundary>
        <App/>
    </ErrorBoundary>
)
