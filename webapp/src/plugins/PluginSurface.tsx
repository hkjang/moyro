import { Component, createElement, isValidElement, type ErrorInfo, type ReactNode } from "react";
import { Provider } from "react-redux";
import type { Store } from "@reduxjs/toolkit";

import type { ReactResolvable } from "./registry";
import { mattermostPluginStore } from "./runtime";

type PluginSurfaceProps = {
  component: ReactResolvable;
  componentProps?: Record<string, unknown>;
  label: string;
};

type BoundaryState = { error: Error | null };

class PluginSurfaceBoundary extends Component<{
  children: ReactNode;
  label: string;
}, BoundaryState> {
  state: BoundaryState = { error: null };

  static getDerivedStateFromError(error: Error): BoundaryState { return { error }; }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error(`plugin surface failed: ${this.props.label}`, error, info);
  }

  render() {
    if (this.state.error) {
      return <div role="alert" className="login-error">{this.props.label} 화면을 표시하지 못했습니다.</div>;
    }
    return this.props.children;
  }
}

export function resolvePluginRenderable(value: ReactResolvable): ReactNode {
  if (isValidElement(value)) return value;
  if (typeof value === "function") return createElement(value as React.ComponentType<Record<string, unknown>>);
  return value as ReactNode;
}

export function PluginSurface({ component, componentProps = {}, label }: PluginSurfaceProps) {
  const content = isValidElement(component)
    ? component
    : typeof component === "function"
      ? createElement(component as React.ComponentType<Record<string, unknown>>, componentProps)
      : component as ReactNode;
  return (
    <PluginSurfaceBoundary label={label}>
      <Provider store={mattermostPluginStore as unknown as Store}>
        <div
          data-plugin-surface={label}
          style={{
            "--button-bg": "#3157d5",
            "--button-bg-rgb": "49, 87, 213",
            "--button-color": "#ffffff",
            "--center-channel-color-rgb": "24, 32, 51",
            "--error-text-color-rgb": "194, 65, 75",
          } as React.CSSProperties}
        >
          {content}
        </div>
      </Provider>
    </PluginSurfaceBoundary>
  );
}
