import PeopleAltOutlined from "@mui/icons-material/PeopleAltOutlined";
import InfoOutlined from "@mui/icons-material/InfoOutlined";
import type { Channel, ChannelStats, User, UserStatusValue } from "@/api/client";
import { WorkspaceAvatar } from "@/features/workspace/sidebar/WorkspaceAvatar";
import { formatDayLabel } from "@/lib/time";

const STATUS_LABEL: Record<UserStatusValue, string> = {
  online: "온라인",
  away: "자리비움",
  dnd: "방해금지",
  offline: "오프라인",
};

/**
 * What an empty conversation says instead of a bare "첫 메시지를 남겨보세요".
 * A room introduces itself — purpose, size, when it was made — and offers the
 * two things a newcomer usually wants next; a direct message names the other
 * person and their presence so the reader knows who they are about to write to.
 */
export function ChannelIntro({ channel, stats, peer, peerStatus, token, onOpenInfo, onOpenMembers }: {
  channel: Channel;
  stats?: ChannelStats;
  /** The other participant for a direct message. */
  peer?: User;
  peerStatus?: UserStatusValue;
  token: string;
  onOpenInfo: () => void;
  onOpenMembers: () => void;
}) {
  if (channel.type === "D") {
    const name = peer?.username ?? "상대";
    return (
      <section className="channel-intro" aria-label="대화 시작">
        <WorkspaceAvatar token={token} id={peer?.id ?? ""} name={name} status={peerStatus} size={56} picture={peer?.picture} updateAt={peer?.update_at} />
        <strong className="channel-intro-title">@{name}</strong>
        <p>{peerStatus ? STATUS_LABEL[peerStatus] : ""}{peer?.email ? ` · ${peer.email}` : ""}</p>
        <p className="channel-intro-hint">여기서 나누는 대화는 두 사람만 볼 수 있습니다. 첫 메시지를 보내 보세요.</p>
      </section>
    );
  }
  const purpose = channel.purpose?.trim() || channel.header?.trim();
  return (
    <section className="channel-intro" aria-label="채널 소개">
      <div className="channel-intro-mark" aria-hidden>#</div>
      <strong className="channel-intro-title">#{channel.display_name || channel.name}</strong>
      <p>
        {channel.type === "P" ? "비공개 채널" : channel.type === "G" ? "그룹 메시지" : "공개 채널"}
        {stats ? ` · 멤버 ${stats.member_count}명` : ""}
        {channel.create_at ? ` · ${formatDayLabel(channel.create_at)} 생성` : ""}
      </p>
      <p className="channel-intro-hint">
        {purpose ? purpose : "아직 설정된 목적이 없습니다. 이 채널의 첫 메시지를 남겨 대화를 시작해 보세요."}
      </p>
      <div className="channel-intro-actions">
        <button type="button" className="context-secondary-button" onClick={onOpenMembers}>
          <PeopleAltOutlined fontSize="inherit" aria-hidden /> 멤버 보기
        </button>
        <button type="button" className="context-secondary-button" onClick={onOpenInfo}>
          <InfoOutlined fontSize="inherit" aria-hidden /> 채널 정보
        </button>
      </div>
    </section>
  );
}
