import type {
  Channel,
  ChannelMember,
  ChannelStats,
  FileInfo,
  PersonalAIPreferences,
  Post,
  Team,
} from "@/api/client";
import { ContextPanel, type WorkspaceContextTab } from "@/features/workspace/context/ContextPanel";
import {
  ChannelFilesView,
  ChannelInfoView,
  ChannelSummaryView,
  EmptyThreadView,
  type ChannelSummarySource,
} from "@/features/workspace/context/ChannelContextViews";
import { ChannelMembersView, ChannelPinnedView } from "@/features/workspace/context/ChannelPeopleViews";
import { ThreadPanel } from "@/features/workspace/thread/ThreadPanel";
import type { ChannelFileEntry } from "@/features/workspace/context/ChannelContextViews";
import type { FilesMap, ReactionMap, StatusMap, UsersMap } from "@/features/workspace/model/types";
import type { ThreadPanel as ThreadPanelState } from "@/features/workspace/model/useThreadPanel";

/** AI summary state, owned by the workspace and rendered in the summary tab. */
export type ChannelSummaryState = {
  permissionLoaded: boolean;
  canUseAI: boolean;
  unavailableReason: string;
  availableMessageCount: number;
  output: string;
  sources: ChannelSummarySource[];
  generatedAt: number | null;
  streaming: boolean;
  error: string;
  onRun: () => void;
  onStop: () => void;
};

export type WorkspaceContextPanelProps = {
  activeTab: WorkspaceContextTab;
  onTabChange: (tab: WorkspaceContextTab) => void;
  onClose: () => void;

  channel: Channel;
  team: Team | null;
  stats?: ChannelStats;
  channelLabel: string;

  token: string;
  currentUserId: string;
  users: UsersMap;
  statuses: StatusMap;
  reactionsByPost: ReactionMap;
  filesByID: FilesMap;

  thread: ThreadPanelState;
  threadComposerResetSeq: number;
  onToggleReaction: (post: Post, emoji: string) => void;
  onEditPost: (postId: string, message: string) => Promise<boolean>;
  onDeletePost: (postId: string) => void;
  onUpload: (files: File[]) => Promise<FileInfo[]>;
  onScheduleThreadReply: (message: string, fileIds: string[]) => void;
  onSendThreadSticker?: (stickerId: string, caption: string) => Promise<boolean>;
  emoticonsEnabled: boolean;

  canUseAI: boolean;
  aiPermissionLoaded: boolean;
  aiStatusLabel: string;
  aiPreferences: PersonalAIPreferences | null;
  summary: ChannelSummaryState;

  fileEntries: ChannelFileEntry[];
  pinned: { posts: Post[]; loading: boolean; error: string };
  members: { members: ChannelMember[]; loading: boolean; error: string };
  onJumpToPost: (postId: string) => void;
  onOpenDirect: (userId: string) => void;
};

/**
 * The right-hand context panel and everything it can show. Extracted from
 * ChatView so the workspace container stays readable: the panel's six tabs
 * need a wide slice of workspace state, but none of the logic behind it.
 */
export function WorkspaceContextPanel(props: WorkspaceContextPanelProps) {
  const {
    activeTab, onTabChange, onClose, channel, team, stats, channelLabel,
    token, currentUserId, users, statuses, reactionsByPost, filesByID,
    thread, threadComposerResetSeq, onToggleReaction, onEditPost, onDeletePost,
    onUpload, onScheduleThreadReply, onSendThreadSticker, emoticonsEnabled,
    canUseAI, aiPermissionLoaded, aiStatusLabel, aiPreferences, summary,
    fileEntries, pinned, members, onJumpToPost, onOpenDirect,
  } = props;

  return (
    <ContextPanel
      activeTab={activeTab}
      onTabChange={onTabChange}
      onClose={onClose}
      panels={{
        thread: thread.rootId ? (
          <ThreadPanel
            rootId={thread.rootId}
            posts={thread.posts}
            loading={thread.loading}
            users={users}
            statuses={statuses}
            reactionsByPost={reactionsByPost}
            filesByID={filesByID}
            currentUserId={currentUserId}
            token={token}
            onToggleReaction={onToggleReaction}
            onEdit={onEditPost}
            onDelete={onDeletePost}
            onReply={thread.reply}
            onUpload={onUpload}
            onSchedule={onScheduleThreadReply}
            onSendSticker={onSendThreadSticker}
            emoticonsEnabled={emoticonsEnabled}
            composerResetSeq={threadComposerResetSeq}
            destinationLabel={`${channelLabel} · 스레드에 답글`}
            canUseAI={canUseAI}
            aiPermissionLoaded={aiPermissionLoaded}
            aiStatusLabel={aiStatusLabel}
            aiPreferences={aiPreferences}
          />
        ) : <EmptyThreadView />,
        summary: (
          <ChannelSummaryView
            permissionLoaded={summary.permissionLoaded}
            canUseAI={summary.canUseAI}
            unavailableReason={summary.unavailableReason}
            availableMessageCount={summary.availableMessageCount}
            output={summary.output}
            sources={summary.sources}
            generatedAt={summary.generatedAt}
            streaming={summary.streaming}
            error={summary.error}
            onRun={summary.onRun}
            onStop={summary.onStop}
            onJumpToPost={onJumpToPost}
          />
        ),
        files: (
          <ChannelFilesView token={token} entries={fileEntries} onJumpToPost={onJumpToPost} />
        ),
        pinned: (
          <ChannelPinnedView
            token={token}
            posts={pinned.posts}
            users={users}
            loading={pinned.loading}
            error={pinned.error}
            onJumpToPost={onJumpToPost}
          />
        ),
        members: (
          <ChannelMembersView
            token={token}
            members={members.members}
            users={users}
            statuses={statuses}
            currentUserId={currentUserId}
            loading={members.loading}
            error={members.error}
            onOpenDirect={onOpenDirect}
          />
        ),
        info: <ChannelInfoView channel={channel} team={team} stats={stats} />,
      }}
    />
  );
}
