import React from 'react';

interface ErrorBoundaryProps {
  children: React.ReactNode;
}

interface ErrorBoundaryState {
  error: Error | null;
}

/**
 * Last line of defense against the Wails "black screen" failure mode.
 *
 * Every prior incident in this repo (missing `window.runtime` at mount,
 * hook-count drift in UpdateBanner, a throwing EventEmitter) ended the
 * same way: an uncaught render error unmounted the React tree, the
 * transparent webview let the zinc-950 BackgroundColour show through,
 * and users saw an unresponsive black window with no signal.
 *
 * This boundary converts that into a visible, actionable fallback with
 * the error message + a one-click reload. It is intentionally a class
 * component — the only place in the codebase where one is warranted,
 * because `componentDidCatch` has no function-component equivalent.
 */
class ErrorBoundary extends React.Component<ErrorBoundaryProps, ErrorBoundaryState> {
  constructor(props: ErrorBoundaryProps) {
    super(props);
    this.state = { error: null };
  }

  static getDerivedStateFromError(error: Error): ErrorBoundaryState {
    return { error };
  }

  componentDidCatch(error: Error, info: React.ErrorInfo): void {
    console.error('ClashGO UI crashed:', error, info.componentStack);
  }

  handleReload = (): void => {
    // Full reload re-runs the theme bootstrap + re-mounts React. If the
    // crash was transient (bridge not injected yet, one bad event
    // payload) this recovers cleanly; if persistent, the boundary
    // catches again rather than black-screening.
    window.location.reload();
  };

  render() {
    if (this.state.error) {
      return (
        <div className="min-h-screen w-full flex items-center justify-center bg-zinc-50 dark:bg-zinc-950 text-zinc-950 dark:text-zinc-50 p-8">
          <div className="max-w-md w-full bg-white dark:bg-zinc-900 rounded-[2rem] border border-zinc-100/50 dark:border-zinc-800/50 shadow-premium-lg p-8 text-center space-y-6">
            <div className="w-16 h-16 mx-auto rounded-2xl bg-rose-500/10 border border-rose-500/30 flex items-center justify-center">
              <span className="material-symbols-outlined text-rose-500 text-3xl">error</span>
            </div>
            <div className="space-y-2">
              <h1 className="font-headline text-xl font-bold tracking-tight">Something went wrong</h1>
              <p className="text-sm text-zinc-400 dark:text-zinc-500 font-medium">
                The interface hit an unexpected error. Reloading usually fixes it.
              </p>
            </div>
            <pre className="max-h-32 overflow-y-auto text-left text-[11px] font-mono text-rose-500/80 bg-rose-500/5 border border-rose-500/20 rounded-xl p-3 break-words whitespace-pre-wrap">
              {this.state.error.message || String(this.state.error)}
            </pre>
            <button
              onClick={this.handleReload}
              className="h-12 w-full rounded-2xl bg-zinc-950 dark:bg-white text-white dark:text-zinc-950 font-black text-[11px] uppercase tracking-[0.3em] transition-all hover:shadow-premium-lg active:scale-[0.98]"
            >
              Reload ClashGO
            </button>
          </div>
        </div>
      );
    }
    return this.props.children;
  }
}

export default ErrorBoundary;
