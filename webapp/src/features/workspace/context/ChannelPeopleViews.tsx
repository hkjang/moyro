import type { ChannelMember, Post, User, UserStatusValue } from "@/api/client";
import { MessageBody } from "@/components/MessageBody";
import { WorkspaceAvatar } from "@/features/workspace/sidebar/WorkspaceAvatar";
import { formatDateTime } from "@/lib/time";

const STATUS_LABEL: Record<UserStatusValue, string> = {
  online: "온라인",
  away: "자리비움",
  dnd: "방해금지",
  offline: "오프라인",
};

const STATUS_ORDER: Record<UserStatusValue, number> = { online: 0, away: 1, dnd: 2, offline: 3 };

function isChannelAdmin(member: ChannelMember): boolean {
  return (member.roles ?? "").split(/\s+/).includes("channel_admin");
}

/** Channel roster with presence, admins first, then by presence and name. */
export function ChannelMembersView({ token, members, users, statuses, currentUserId, loading, error, onOpenDirect }: {
  token: string;
  members: ChannelMember[];
  users: Record<string, User>;
  statuses: Record<string, UserStatusValue>;
  currentUserId: string;
  loading: boolean;
  error: string;
  onOpenDirect?: (userId: string) => void;
}) {
  const rows = [...members].sort((a, b) => {
    const adminDelta = Number(isChannelAdmin(b)) - Number(isChannelAdmin(a));
    if (adminDelta !== 0) return adminDelta;
    const statusDelta = STATUS_ORDER[statuses[a.user_id] ?? "offline"] - STATUS_ORDER[statuses[b.user_id] ?? "offline"];
    if (statusDelta !== 0) return statusDelta;
    return (users[a.user_id]?.username ?? "").localeCompare(users[b.user_id]?.username ?? "", "ko");
  });
  const online = members.filter((m) => (statuses[m.user_id] ?? "offline") !== "offline").length;

  return (
    <section className="context-view" aria-label="채널 멤버">
      <div className="context-view-heading">
        <div>
          <h3>멤버</h3>
          <p>{members.length}명 · 온라인 {online}명</p>
        </div>
      </div>
      {error && <div className="context-error" role="alert">{error}</div>}
      {loading && members.length === 0 ? (
        <div className="context-state">멤버를 불러오는 중…</div>
      ) : (
        <ul className="context-member-list">
          {rows.map((member) => {
            const user = users[member.user_id];
            const status = statuses[member.user_id] ?? "offline";
            const me = member.user_id === currentUserId;
            return (
              <li key={member.user_id} className="context-member-item">
                <WorkspaceAvatar
                  token={token}
                  id={member.user_id}
                  name={user?.username ?? ""}
                  status={status}
                  size={28}
                  picture={user?.picture}
                  updateAt={user?.update_at}
                />
                <div className="context-member-detail">
                  <strong>
                    {user?.username ?? member.user_id.slice(0, 8)}
                    {me && <span className="context-member-me"> (나)</span>}
                  </strong>
                  <span>
                    {STATUS_LABEL[status]}
                    {isChannelAdmin(member) ? " · 채널 관리자" : ""}
                  </span>
                </div>
                {!me && onOpenDirect && (
                  <button
                    type="button"
                    className="context-secondary-button"
                    onClick={() => onOpenDirect(member.user_id)}
                    aria-label={`${user?.username ?? "멤버"}에게 다이렉트 메시지`}
                  >
                    DM
                  </button>
                )}
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

/** Pinned posts, newest first, each linking back to its place in the channel. */
export function ChannelPinnedView({ token, posts, users, loading, error, onJumpToPost }: {
  token: string;
  posts: Post[];
  users: Record<string, User>;
  loading: boolean;
  error: string;
  onJumpToPost: (postId: string) => void;
}) {
  return (
    <section className="context-view" aria-label="고정 메시지">
      <div className="context-view-heading">
        <div>
          <h3>고정 메시지</h3>
          <p>이 채널에 고정된 메시지 {posts.length}개</p>
        </div>
      </div>
      {error && <div className="context-error" role="alert">{error}</div>}
      {loading && posts.length === 0 ? (
        <div className="context-state">고정 메시지를 불러오는 중…</div>
      ) : posts.length === 0 ? (
        <div className="context-state">고정된 메시지가 없습니다. 메시지 메뉴에서 고정할 수 있습니다.</div>
      ) : (
        <ul className="context-pinned-list">
          {posts.map((post) => (
            <li key={post.id} className="context-pinned-item">
              <div className="context-pinned-meta">
                <strong>{users[post.user_id]?.username ?? post.user_id.slice(0, 8)}</strong>
                <span>{formatDateTime(post.create_at)}</span>
              </div>
              <MessageBody source={post.message} token={token} />
              <button type="button" className="context-source-link" onClick={() => onJumpToPost(post.id)}>
                메시지로 이동
              </button>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
