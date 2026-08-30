import CloseRounded from "@mui/icons-material/CloseRounded";

import type { RhsComponent } from "./registry";
import { PluginSurface, resolvePluginRenderable } from "./PluginSurface";

export function PluginRHSPanel({ registration, onClose }: {
  registration: RhsComponent;
  onClose: () => void;
}) {
  return (
    <aside className="thread-panel context-panel" aria-label={`${registration.pluginId} 플러그인 패널`}>
      <header className="thread-header context-panel-header">
        <strong style={{ minWidth: 0 }}>{resolvePluginRenderable(registration.title)}</strong>
        <button
          type="button"
          className="action-btn context-panel-close"
          onClick={onClose}
          aria-label="플러그인 패널 닫기"
          title="닫기"
        >
          <CloseRounded fontSize="inherit" aria-hidden />
        </button>
      </header>
      <div className="context-panel-content moyro-scrollbar">
        <PluginSurface
          component={registration.component}
          label={`${registration.pluginId} RHS`}
        />
      </div>
    </aside>
  );
}
