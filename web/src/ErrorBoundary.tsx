import { Component, ReactNode } from "react";

interface Props {
  children: ReactNode;
}

interface State {
  hasError: boolean;
  error?: Error;
}

export class ErrorBoundary extends Component<Props, State> {
  constructor(props: Props) {
    super(props);
    this.state = { hasError: false };
  }

  static getDerivedStateFromError(error: Error): State {
    return { hasError: true, error };
  }

  componentDidCatch(error: Error, info: React.ErrorInfo) {
    console.error("[ErrorBoundary] Render error:", error, info);
  }

  render() {
    if (this.state.hasError) {
      return (
        <div className="content">
          <div className="sec">
            <span className="ix">!</span>
            <span className="t rose">Render error</span>
          </div>
          <p style={{ padding: "0 22px", color: "var(--dim)", fontSize: 13 }}>
            Something went wrong rendering this page. Reload.
          </p>
        </div>
      );
    }
    return this.props.children;
  }
}
