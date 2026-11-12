import React from 'react';
import Icon from './ui/Icon';

interface ErrorBoundaryProps {
  children: React.ReactNode;
}

interface ErrorBoundaryState {
  error: Error | null;
}

/**
 * Last line of defense against the Wails "black screen" failure mode: an
 * uncaught render error would unmount the tree and leave the transparent
 * webview showing the window background. This turns that into a readable,
 * actionable fallback. Class component because `componentDidCatch` has no
 * function-component equivalent.
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
    window.location.reload();
  };

  render() {
    if (this.state.error) {
      return (
        <div className="crash">
          <div className="crash-card">
            <div className="crash-ico"><Icon name="alert" size={22} /></div>
            <h1>Something went wrong</h1>
            <p>The interface hit an unexpected error. Reloading usually fixes it.</p>
            <pre>{this.state.error.message || String(this.state.error)}</pre>
            <button type="button" onClick={this.handleReload} className="btn primary block">
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
