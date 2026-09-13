import React from 'react'

interface State { error: Error | null }

/** Keeps a render error from blanking the whole native window. */
export default class ErrorBoundary extends React.Component<React.PropsWithChildren, State> {
  state: State = { error: null }
  static getDerivedStateFromError(error: Error): State { return { error } }
  componentDidCatch(error: Error, info: React.ErrorInfo) { console.error(error, info.componentStack) }
  render() {
    if (this.state.error) {
      return (
        <div className="crash">
          <h2>Интерфейс упал</h2>
          <pre>{String(this.state.error?.stack || this.state.error)}</pre>
          <button className="primary" onClick={() => this.setState({ error: null })}>Попробовать снова</button>
        </div>
      )
    }
    return this.props.children
  }
}
