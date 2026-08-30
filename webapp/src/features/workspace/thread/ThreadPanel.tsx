import type { FileInfo, PersonalAIPreferences, Post } from "@/api/client";
import { MessageComposer } from "@/features/workspace/composer/MessageComposer";
import { MessageItem } from "@/features/workspace/messages/MessageItem";
import type {
  FilesMap,
  ReactionMap,
  StatusMap,
  UsersMap,
} from "@/features/workspace/model/types";

export function TypingIndicator({ typingUsers, users }: { typingUsers: string[]; users: UsersMap }) {
  if (typingUsers.length === 0) return null;
  const names = typingUsers.map((uid) => users[uid]?.username ?? uid.slice(0, 6)).slice(0, 3);
  const label = names.length === 1
    ? `${names[0]}님이 입력 중…`
    : names.length <= 3
      ? `${names.join(", ")}님이 입력 중…`
      : "여러 명이 입력 중…";
  return <div className="typing-indicator">{label}</div>;
}

type ThreadPanelProps = {
  rootId: string;
  posts: Post[];
  loading: boolean;
  users: UsersMap;
  statuses: StatusMap;
  reactionsByPost: ReactionMap;
  filesByID: FilesMap;
  currentUserId: string;
  token: string;
  onToggleReaction: (post: Post, emoji: string) => void;
  onEdit: (postId: string, message: string) => Promise<boolean>;
  onDelete: (postId: string) => void;
  onReply: (message: string, fileIds: string[]) => Promise<boolean>;
  onUpload: (files: File[]) => Promise<FileInfo[]>;
  // Phase 20 (F7) — thread schedule parity. Thread replies can now be
  // scheduled because the server already supports root_id on
  // scheduled_posts; we just had to pipe it through.
  onSchedule?: (message: string, fileIds: string[]) => void;
  composerResetSeq?: number;
  destinationLabel: string;
  canUseAI: boolean;
  aiPermissionLoaded: boolean;
  aiStatusLabel: string;
  aiPreferences: PersonalAIPreferences | null;
};

export function ThreadPanel(props: ThreadPanelProps) {
  const {
    rootId, posts, loading, users, statuses, reactionsByPost, filesByID,
    currentUserId, token, onToggleReaction, onEdit, onDelete, onReply, onUpload,
    onSchedule, composerResetSeq, destinationLabel, canUseAI, aiPermissionLoaded,
    aiStatusLabel, aiPreferences,
  } = props;

  const root = posts.find((p) => p.id === rootId) ?? null;
  const replies = posts.filter((p) => p.id !== rootId);

  return (
    <>
      <div className="thread-body">
        {loading && posts.length === 0 ? (
          <div className="chat-empty">불러오는 중…</div>
        ) : !root ? (
          <div className="chat-empty">원본 메시지를 찾을 수 없습니다.</div>
        ) : (
          <>
            <MessageItem
              post={root}
              isMe={root.user_id === currentUserId}
              author={users[root.user_id]}
              status={statuses[root.user_id]}
              reactions={reactionsByPost[root.id] ?? []}
              currentUserId={currentUserId}
              files={(root.file_ids ?? []).map((id) => filesByID[id]).filter(Boolean) as FileInfo[]}
              token={token}
              onToggleReaction={(emoji) => onToggleReaction(root, emoji)}
              onEdit={onEdit}
              onDelete={onDelete}
              hideThreadAction
            />
            <div className="thread-divider">답글 {replies.length}개</div>
            {replies.map((p) => (
              <MessageItem
                key={p.id}
                post={p}
                isMe={p.user_id === currentUserId}
                author={users[p.user_id]}
                status={statuses[p.user_id]}
                reactions={reactionsByPost[p.id] ?? []}
                currentUserId={currentUserId}
                files={(p.file_ids ?? []).map((id) => filesByID[id]).filter(Boolean) as FileInfo[]}
                token={token}
                onToggleReaction={(emoji) => onToggleReaction(p, emoji)}
                onEdit={onEdit}
                onDelete={onDelete}
                hideThreadAction
              />
            ))}
          </>
        )}
      </div>
      <MessageComposer
        token={token}
        // Thread replies belong to the root post's channel; fall back to
        // null if the root hasn't loaded yet so the autocomplete hook
        // stays dormant instead of querying an empty channelID.
        channelID={root?.channel_id ?? null}
        destinationLabel={destinationLabel}
        canUseAI={canUseAI}
        aiPermissionLoaded={aiPermissionLoaded}
        aiStatusLabel={aiStatusLabel}
        aiPreferences={aiPreferences}
        onSend={onReply}
        onTyping={() => { /* typing in threads is best-effort; skip for now */ }}
        onUpload={onUpload}
        userId={currentUserId}
        rootId={rootId}
        onSchedule={onSchedule}
        resetSeq={composerResetSeq}
      />
    </>
  );
}

// ---- Phase 18: ChannelDiscoverModal ----
//
// Lists public channels in the current team that the user hasn't joined.
// Debounced text search; clicking 참여 calls joinChannel and removes the
// row optimistically. Pagination is an "더 보기" button rather than infinite
// scroll — the list is bounded and a single expansion is usually enough.
