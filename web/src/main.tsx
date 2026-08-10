import {createRoot} from 'react-dom/client'
import './style.css'
import App from './App'
import ErrorBoundary from './components/ErrorBoundary'

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
