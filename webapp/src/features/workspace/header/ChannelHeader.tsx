import { createElement, isValidElement, useEffect, useRef, useState } from "react";
import type { ComponentType, ReactNode } from "react";
import AttachFileRounded from "@mui/icons-material/AttachFileRounded";
import CloseRounded from "@mui/icons-material/CloseRounded";
import InfoOutlined from "@mui/icons-material/InfoOutlined";
import SummarizeRounded from "@mui/icons-material/SummarizeRounded";
import type {
  Channel,
  ChannelNotifyProps,
  ChannelStats,
  Team,
  User,
  UserStatusValue,
} from "@/api/client";
import { directMessagePeerID } from "@/features/workspace/sidebar/WorkspaceSidebar";
import { WorkspaceAvatar } from "@/features/workspace/sidebar/WorkspaceAvatar";
import type { WorkspaceContextTab } from "@/features/workspace/context/ContextPanel";
import { usePluginRegistryState } from "@/plugins/registry";
import type { ReactResolvable } from "@/plugins/registry";

type ChannelHeaderProps = {
  token: string | null;
  currentUser: User | null;
  team: Team | null;
  channel: Channel;
  users: Record<string, User>;
  statuses: Record<string, UserStatusValue>;
  status: UserStatusValue;
  stats?: ChannelStats;
  notifyProps: ChannelNotifyProps;
  isAdmin: boolean;
  searchTerm: string;
  searchOpen: boolean;
  accountMenuOpen: boolean;
  activeContext: WorkspaceContextTab | null;
  onChangeNotify: (patch: Partial<ChannelNotifyProps>) => void;
  onArchive: () => void;
  onSearchTermChange: (value: string) => void;
  onSearch: () => void;
  onClearSearch: () => void;
  onToggleAccountMenu: () => void;
  onOpenContext: (tab: Exclude<WorkspaceContextTab, "thread">) => void;
};

type DesktopPref = "all" | "mentions" | "none";
type MarkUnreadPref = "all" | "mention";

function resolvePluginIcon(icon: ReactResolvable): ReactNode {
  if (isValidElement(icon)) return icon;
  if (typeof icon === "function") return createElement(icon as ComponentType);
  return icon as ReactNode;
}

function ChannelSettingsMenu({
  props,
  onChange,
}: {
  props: ChannelNotifyProps;
  onChange: (patch: Partial<ChannelNotifyProps>) => void;
}) {
  const [open, setOpen] = useState(false);
  const wrapRef = useRef<HTMLSpanElement>(null);

  useEffect(() => {
    if (!open) return;
    function onDocumentMouseDown(event: MouseEvent) {
      if (!wrapRef.current) return;
      if (!wrapRef.current.contains(event.target as Node)) setOpen(false);
    }
    document.addEventListener("mousedown", onDocumentMouseDown);
    return () => document.removeEventListener("mousedown", onDocumentMouseDown);
  }, [open]);

  const desktop = (props.desktop ?? "all") as DesktopPref;
  const markUnread = (props.mark_unread ?? "all") as MarkUnreadPref;

  return (
    <span className="settings-wrap" ref={wrapRef}>
      <button
        type="button"
        className="settings-gear"
        title="채널 알림 설정"
        aria-label="채널 알림 설정"
        onClick={() => setOpen((value) => !value)}
      >
        ⚙
      </button>
      {open && (
        <div className="notify-menu" role="dialog" aria-label="알림 설정">
          <div className="notify-section-title">데스크톱 알림</div>
          {(["all", "mentions", "none"] as DesktopPref[]).map((value) => (
            <label key={value} className="notify-radio">
              <input
                type="radio"
                name="desktop"
                checked={desktop === value}
                onChange={() => onChange({ desktop: value })}
              />
              <span>
                {value === "all"
                  ? "모든 새 메시지"
                  : value === "mentions"
                    ? "@멘션 또는 DM만"
                    : "끄기"}
              </span>
            </label>
          ))}
          <div className="notify-section-title" style={{ marginTop: 10 }}>읽지 않음 표시</div>
          {(["all", "mention"] as MarkUnreadPref[]).map((value) => (
            <label key={value} className="notify-radio">
              <input
                type="radio"
                name="mark_unread"
                checked={markUnread === value}
                onChange={() => onChange({ mark_unread: value })}
              />
              <span>{value === "all" ? "모든 메시지" : "멘션만 (음소거)"}</span>
            </label>
          ))}
        </div>
      )}
    </span>
  );
}

export function ChannelHeader(props: ChannelHeaderProps) {
  const pluginRegistry = usePluginRegistryState();
  const {
    token,
    currentUser,
    team,
    channel,
    users,
    statuses,
    status,
    stats,
    notifyProps,
    isAdmin,
    searchTerm,
    searchOpen,
    accountMenuOpen,
    activeContext,
    onChangeNotify,
    onArchive,
    onSearchTermChange,
    onSearch,
    onClearSearch,
    onToggleAccountMenu,
    onOpenContext,
  } = props;
  const peerID = channel.type === "D"
    ? directMessagePeerID(channel.name, currentUser?.id ?? "")
    : "";
  const peer = peerID ? users[peerID] : undefined;

  return (
    <header className="chat-header">
      <div className="chat-header-left">
        <div className="chat-header-team">{team?.display_name}</div>
        <h2 className="chat-header-title">
          {channel.type === "D" ? (
            <>
              <WorkspaceAvatar
                token={token}
                id={peerID}
                name=""
                status={statuses[peerID]}
                size={22}
                picture={peer?.picture}
                updateAt={peer?.update_at}
              />
              {" "}
              {peer?.username ?? "다이렉트 메시지"}
            </>
          ) : (
            <><span className="channel-hash">#</span>{channel.display_name}</>
          )}
          {channel.type !== "D" && stats && (
            <span
              className="channel-stats-chip"
              title={`멤버 ${stats.member_count}명 · 고정 ${stats.pinnedpost_count}개 · 파일 ${stats.files_count}개`}
            >
              👥 {stats.member_count}
            </span>
          )}
          <ChannelSettingsMenu props={notifyProps} onChange={onChangeNotify} />
          {isAdmin && channel.type !== "D" && channel.type !== "G" && (
            <button
              type="button"
              className="action-btn"
              title="채널 보관"
              style={{ marginLeft: 6 }}
              onClick={onArchive}
            >
              🗄️
            </button>
          )}
        </h2>
      </div>
      <div className="chat-header-right">
        <div className="channel-context-actions" aria-label="채널 컨텍스트 열기">
          {pluginRegistry.channelHeaderButtons.map((button) => (
            <button
              key={button.id}
              type="button"
              className="channel-context-action"
              aria-label={button.tooltipText || button.dropdownText}
              title={button.tooltipText || button.dropdownText}
              onClick={() => button.action(channel)}
            >
              <span aria-hidden>{resolvePluginIcon(button.icon)}</span>
              <span>{button.dropdownText}</span>
            </button>
          ))}
          <button
            type="button"
            className={`channel-context-action ${activeContext === "summary" ? "is-active" : ""}`}
            aria-label="AI 요약 패널 열기"
            aria-pressed={activeContext === "summary"}
            onClick={() => onOpenContext("summary")}
          >
            <SummarizeRounded fontSize="inherit" aria-hidden />
            <span>요약</span>
          </button>
          <button
            type="button"
            className={`channel-context-action ${activeContext === "files" ? "is-active" : ""}`}
            aria-label="파일 패널 열기"
            aria-pressed={activeContext === "files"}
            onClick={() => onOpenContext("files")}
          >
            <AttachFileRounded fontSize="inherit" aria-hidden />
            <span>파일</span>
          </button>
          <button
            type="button"
            className={`channel-context-action ${activeContext === "info" ? "is-active" : ""}`}
            aria-label="채널 정보 패널 열기"
            aria-pressed={activeContext === "info"}
            onClick={() => onOpenContext("info")}
          >
            <InfoOutlined fontSize="inherit" aria-hidden />
            <span>정보</span>
          </button>
        </div>
        <form
          className="search-form"
          onSubmit={(event) => {
            event.preventDefault();
            onSearch();
          }}
        >
          <span className="search-icon" aria-hidden>🔍</span>
          <input
            className="search-input"
            placeholder="메시지 검색"
            value={searchTerm}
            onChange={(event) => onSearchTermChange(event.target.value)}
            title="from:username, in:channel, before:YYYY-MM-DD, after:YYYY-MM-DD, has:file, has:link"
          />
          {searchOpen && (
            <button
              type="button"
              className="search-clear"
              aria-label="검색 닫기"
              title="검색 닫기"
              onClick={onClearSearch}
            >
              <CloseRounded fontSize="inherit" aria-hidden />
            </button>
          )}
        </form>
        <span className="chat-header-divider" aria-hidden />
        <button
          type="button"
          className="user-menu-trigger"
          aria-label="계정 메뉴 열기"
          aria-expanded={accountMenuOpen}
          aria-haspopup="menu"
          onClick={onToggleAccountMenu}
          title="계정 · 프로필 · 환경설정"
        >
          <WorkspaceAvatar
            token={token}
            id={currentUser?.id ?? ""}
            name={currentUser?.username ?? ""}
            status={status}
            size={28}
            picture={currentUser?.picture}
            updateAt={currentUser?.update_at}
          />
          <span className="user-menu-caret" aria-hidden>▾</span>
        </button>
      </div>
    </header>
  );
}
