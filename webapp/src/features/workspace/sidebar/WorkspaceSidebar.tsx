import CloseRounded from "@mui/icons-material/CloseRounded";
import StarBorderRounded from "@mui/icons-material/StarBorderRounded";
import StarRounded from "@mui/icons-material/StarRounded";
import type { Channel, Team, User, UserStatusValue } from "@/api/client";
import { BrandMark } from "@/components/brand/BrandMark";
import { WorkspaceAvatar } from "@/features/workspace/sidebar/WorkspaceAvatar";

export type WorkspaceUnreadEntry = { msg: number; mention: number };

type WorkspaceSidebarProps = {
  token: string | null;
  currentUser: User | null;
  teams: Team[];
  currentTeamId: string | null;
  currentChannelId: string | null;
  favoriteChannels: Channel[];
  publicChannels: Channel[];
  directChannels: Channel[];
  archivedChannels: Channel[];
  users: Record<string, User>;
  statuses: Record<string, UserStatusValue>;
  unread: Record<string, WorkspaceUnreadEntry>;
  scheduledCount: number;
  showArchived: boolean;
  isAdmin: boolean;
  onSelectTeam: (teamId: string) => void;
  onSelectChannel: (channelId: string) => void;
  onCreateTeam: () => void;
  onCreateChannel: () => void;
  onOpenSaved: () => void;
  onOpenScheduled: () => void;
  onOpenDiscover: () => void;
  onToggleArchived: () => void;
  onRestoreChannel: (channelId: string) => void;
  onOpenDirect: () => void;
  onToggleFavorite: (channelId: string) => void;
  onOpenAdmin: () => void;
  onCloseMobile: () => void;
};

function sectionColor(id: string): string {
  const palette = ["#6366f1", "#8b5cf6", "#ec4899", "#f59e0b", "#10b981", "#06b6d4"];
  let hash = 0;
  for (let index = 0; index < id.length; index += 1) {
    hash = (hash * 31 + id.charCodeAt(index)) >>> 0;
  }
  return palette[hash % palette.length];
}

export function directMessagePeerID(name: string, currentUserID: string): string {
  const [first, second] = name.split("__");
  if (!second) return first ?? "";
  return first === currentUserID ? second : first;
}

function SectionTitle({ children }: { children: React.ReactNode }) {
  return <div className="section-title">{children}</div>;
}

type ChannelRowProps = {
  channel: Channel;
  active: boolean;
  unread: WorkspaceUnreadEntry;
  onClick: () => void;
  isFavorite?: boolean;
  onToggleFavorite?: (channelId: string) => void;
};

function FavoriteButton({ channel, favorite, onToggle }: {
  channel: Channel;
  favorite: boolean;
  onToggle: (channelId: string) => void;
}) {
  const action = favorite ? "즐겨찾기 해제" : "즐겨찾기에 추가";
  return (
    <button
      type="button"
      className={`channel-fav workspace-channel-favorite ${favorite ? "is-fav" : ""}`}
      aria-label={`${channel.display_name} ${action}`}
      title={action}
      onClick={() => onToggle(channel.id)}
    >
      {favorite
        ? <StarRounded fontSize="inherit" aria-hidden />
        : <StarBorderRounded fontSize="inherit" aria-hidden />}
    </button>
  );
}

function ChannelRow({
  channel,
  active,
  unread,
  onClick,
  isFavorite,
  onToggleFavorite,
}: ChannelRowProps) {
  return (
    <div className={`workspace-channel-row ${active ? "is-active" : ""}`}>
      <button
        type="button"
        className={`item workspace-channel-select ${active ? "item-active" : ""}`}
        aria-current={active ? "page" : undefined}
        onClick={onClick}
      >
        <span className="channel-hash">#</span>
        <span className="workspace-channel-label">{channel.display_name}</span>
        {unread.mention > 0
          ? <span className="mention-badge">{unread.mention}</span>
          : unread.msg > 0
            ? <span className="unread">{unread.msg}</span>
            : null}
      </button>
      {onToggleFavorite && (
        <FavoriteButton channel={channel} favorite={isFavorite === true} onToggle={onToggleFavorite} />
      )}
    </div>
  );
}

function DirectChannelRow({
  channel,
  currentUser,
  users,
  statuses,
  token,
  unread,
  active,
  favorite,
  onSelect,
  onToggleFavorite,
}: {
  channel: Channel;
  currentUser: User | null;
  users: Record<string, User>;
  statuses: Record<string, UserStatusValue>;
  token: string | null;
  unread: WorkspaceUnreadEntry;
  active: boolean;
  favorite: boolean;
  onSelect: () => void;
  onToggleFavorite: (channelId: string) => void;
}) {
  const peerID = directMessagePeerID(channel.name, currentUser?.id ?? "");
  const peer = users[peerID];
  return (
    <div className={`workspace-channel-row ${active ? "is-active" : ""}`}>
      <button
        type="button"
        className={`item workspace-channel-select ${active ? "item-active" : ""}`}
        aria-current={active ? "page" : undefined}
        onClick={onSelect}
      >
        <WorkspaceAvatar
          token={token}
          id={peerID}
          name={peer?.username ?? peerID.slice(0, 8)}
          status={statuses[peerID]}
          size={22}
          picture={peer?.picture}
          updateAt={peer?.update_at}
        />
        <span className="workspace-channel-label workspace-direct-channel-label">
          {peer?.username ?? peerID.slice(0, 8)}
        </span>
        {unread.mention > 0
          ? <span className="mention-badge">{unread.mention}</span>
          : unread.msg > 0
            ? <span className="unread">{unread.msg}</span>
            : null}
      </button>
      <FavoriteButton channel={channel} favorite={favorite} onToggle={onToggleFavorite} />
    </div>
  );
}

export function WorkspaceSidebar(props: WorkspaceSidebarProps) {
  const {
    token,
    currentUser,
    teams,
    currentTeamId,
    currentChannelId,
    favoriteChannels,
    publicChannels,
    directChannels,
    archivedChannels,
    users,
    statuses,
    unread,
    scheduledCount,
    showArchived,
    isAdmin,
    onSelectTeam,
    onSelectChannel,
    onCreateTeam,
    onCreateChannel,
    onOpenSaved,
    onOpenScheduled,
    onOpenDiscover,
    onToggleArchived,
    onRestoreChannel,
    onOpenDirect,
    onToggleFavorite,
    onOpenAdmin,
    onCloseMobile,
  } = props;

  const channelIsActive = (channelID: string) => channelID === currentChannelId;

  return (
    <aside id="workspace-sidebar" className="chat-side workspace-sidebar" aria-label="워크스페이스 탐색">
      <div className={`side-brand ${isAdmin ? "" : "side-brand-no-burger"}`}>
        {isAdmin && (
          <button
            type="button"
            className="side-hamburger"
            aria-label="운영 관리 열기"
            title="운영 관리 · 시스템 · 플러그인 · 역할 · 작업"
            onClick={onOpenAdmin}
          >
            <span /><span /><span />
          </button>
        )}
        <div className="side-brand-name">
          <BrandMark className="side-brand-logo" size={30} />
          <strong>moyro</strong>
        </div>
        <button
          type="button"
          className="workspace-mobile-sidebar-close"
          data-workspace-sidebar-close
          aria-label="채널 탐색 닫기"
          title="채널 탐색 닫기"
          onClick={onCloseMobile}
        >
          <CloseRounded fontSize="inherit" aria-hidden />
        </button>
      </div>

      <SectionTitle>팀</SectionTitle>
      <div className="item-list">
        {teams.map((team) => (
          <button
            key={team.id}
            className={`item ${team.id === currentTeamId ? "item-active" : ""}`}
            onClick={() => onSelectTeam(team.id)}
          >
            <span className="item-badge" style={{ background: sectionColor(team.id) }}>
              {team.display_name[0]?.toUpperCase() ?? "?"}
            </span>
            {team.display_name}
          </button>
        ))}
        <button className="item item-muted" onClick={() => { onCloseMobile(); onCreateTeam(); }}>＋ 새 팀</button>
      </div>

      {currentTeamId && (
        <>
          <div className="item-list" style={{ marginBottom: 4 }}>
            <button
              type="button"
              className="item"
              onClick={() => { onCloseMobile(); onOpenSaved(); }}
              title="북마크한 메시지 모아보기"
            >
              ⭐ 저장됨
            </button>
            <button
              type="button"
              className="item"
              onClick={() => { onCloseMobile(); onOpenScheduled(); }}
              title="예약된 메시지"
              style={{ display: "flex", alignItems: "center", gap: 6 }}
            >
              <span style={{ flex: 1 }}>🕐 예약됨</span>
              {scheduledCount > 0 && (
                <span className="unread-badge" aria-label={`예약 ${scheduledCount}건`}>
                  {scheduledCount}
                </span>
              )}
            </button>
          </div>

          {favoriteChannels.length > 0 && (
            <>
              <SectionTitle>⭐ 즐겨찾기</SectionTitle>
              <div className="item-list">
                {favoriteChannels.map((channel) => (
                  channel.type === "D" ? (
                    <DirectChannelRow
                      key={channel.id}
                      channel={channel}
                      currentUser={currentUser}
                      users={users}
                      statuses={statuses}
                      token={token}
                      unread={unread[channel.id] ?? { msg: 0, mention: 0 }}
                      active={channelIsActive(channel.id)}
                      favorite
                      onSelect={() => onSelectChannel(channel.id)}
                      onToggleFavorite={onToggleFavorite}
                    />
                  ) : (
                    <ChannelRow
                      key={channel.id}
                      channel={channel}
                      active={channelIsActive(channel.id)}
                      unread={unread[channel.id] ?? { msg: 0, mention: 0 }}
                      onClick={() => onSelectChannel(channel.id)}
                      isFavorite
                      onToggleFavorite={onToggleFavorite}
                    />
                  )
                ))}
              </div>
            </>
          )}

          <SectionTitle>채널</SectionTitle>
          <div className="item-list">
            {publicChannels.map((channel) => (
              <ChannelRow
                key={channel.id}
                channel={channel}
                active={channelIsActive(channel.id)}
                unread={unread[channel.id] ?? { msg: 0, mention: 0 }}
                onClick={() => onSelectChannel(channel.id)}
                isFavorite={false}
                onToggleFavorite={onToggleFavorite}
              />
            ))}
            <button className="item item-muted" onClick={() => { onCloseMobile(); onCreateChannel(); }}>＋ 새 채널</button>
            <button
              className="item item-muted"
              onClick={() => { onCloseMobile(); onOpenDiscover(); }}
              title="가입 가능한 공개 채널 찾아보기"
            >
              🔍 채널 탐색
            </button>
            <button
              className="item item-muted"
              onClick={onToggleArchived}
              title="보관된 채널 표시/숨김"
            >
              {showArchived ? "▴ 보관된 채널 숨기기" : "▾ 보관된 채널 보기"}
            </button>
            {showArchived && archivedChannels.map((channel) => (
              <div
                key={channel.id}
                className="item"
                style={{ opacity: 0.55, display: "flex", alignItems: "center", gap: 6 }}
              >
                <span style={{ flex: 1, fontStyle: "italic" }}>
                  # {channel.display_name}
                </span>
                {isAdmin && (
                  <button
                    type="button"
                    className="action-btn"
                    title="복원"
                    onClick={() => onRestoreChannel(channel.id)}
                  >
                    ↺
                  </button>
                )}
              </div>
            ))}
            {showArchived && archivedChannels.length === 0 && (
              <div className="item item-muted" style={{ fontSize: 13 }}>
                보관된 채널이 없습니다.
              </div>
            )}
          </div>

          <SectionTitle>다이렉트 메시지</SectionTitle>
          <div className="item-list">
            {directChannels.map((channel) => (
              <DirectChannelRow
                key={channel.id}
                channel={channel}
                currentUser={currentUser}
                users={users}
                statuses={statuses}
                token={token}
                unread={unread[channel.id] ?? { msg: 0, mention: 0 }}
                active={channelIsActive(channel.id)}
                favorite={false}
                onSelect={() => onSelectChannel(channel.id)}
                onToggleFavorite={onToggleFavorite}
              />
            ))}
            <button className="item item-muted" onClick={() => { onCloseMobile(); onOpenDirect(); }}>＋ 새 DM</button>
          </div>
        </>
      )}
    </aside>
  );
}
