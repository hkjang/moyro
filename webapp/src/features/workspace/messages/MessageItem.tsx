import { useId, useMemo, useRef, useState } from "react";
import AddReactionRounded from "@mui/icons-material/AddReactionRounded";
import AssignmentTurnedInRounded from "@mui/icons-material/AssignmentTurnedInRounded";
import AttachFileRounded from "@mui/icons-material/AttachFileRounded";
import ChatBubbleOutlineRounded from "@mui/icons-material/ChatBubbleOutlineRounded";
import DeleteOutlineRounded from "@mui/icons-material/DeleteOutlineRounded";
import EditOutlined from "@mui/icons-material/EditOutlined";
import GavelRounded from "@mui/icons-material/GavelRounded";
import MoreHorizRounded from "@mui/icons-material/MoreHorizRounded";
import NotificationsNoneRounded from "@mui/icons-material/NotificationsNoneRounded";
import PushPinRounded from "@mui/icons-material/PushPinRounded";
import StarBorderRounded from "@mui/icons-material/StarBorderRounded";
import StarRounded from "@mui/icons-material/StarRounded";
import { Menu, MenuItem } from "@mui/material";
import type { FileInfo, Post, Reaction, User, UserStatusValue } from "@/api/client";
import { api } from "@/api/client";
import { EmojiPicker, customEmojiByName } from "@/components/EmojiPicker";
import { AuthenticatedImage, downloadAuthenticatedMedia } from "@/components/AuthenticatedMedia";
import { Lightbox } from "@/components/Lightbox";
import { MessageBody } from "@/components/MessageBody";
import { useMentionAutocomplete } from "@/components/MentionPicker";
import { useDraft } from "@/features/workspace/composer/useDraft";
import { useWorkItemCreation } from "@/features/work-items/WorkItemCreationProvider";
import { WorkspaceAvatar } from "@/features/workspace/sidebar/WorkspaceAvatar";
import { PluginSurface } from "@/plugins/PluginSurface";
import { usePluginRegistryState } from "@/plugins/registry";
import "@/features/workspace/messages/message-item.css";

const QUICK_EMOJIS = ["+1", "heart", "tada", "laughing", "eyes", "rocket"];

const EMOJI_MAP: Record<string, string> = {
  "+1": "👍",
  "-1": "👎",
  heart: "❤️",
  tada: "🎉",
  laughing: "😄",
  eyes: "👀",
  rocket: "🚀",
  fire: "🔥",
  clap: "👏",
  check: "✅",
};

function emojiCharacter(name: string): string {
  return EMOJI_MAP[name] ?? `:${name}:`;
}

function formatMessageTime(value: number): string {
  return new Date(value).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

function humanFileSize(bytes: number): string {
  if (!bytes) return "";
  const units = ["B", "KB", "MB", "GB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value.toFixed(value < 10 && unit > 0 ? 1 : 0)}${units[unit]}`;
}

export type MessageItemProps = {
  post: Post;
  isMe: boolean;
  author?: User;
  status?: UserStatusValue;
  reactions: Reaction[];
  currentUserId: string;
  files: FileInfo[];
  token: string;
  onToggleReaction: (emoji: string) => void;
  onEdit: (postId: string, message: string) => Promise<boolean>;
  onDelete: (postId: string) => void;
  onOpenThread?: (rootId: string) => void;
  domAnchorId?: string;
  compact?: boolean;
  hideThreadAction?: boolean;
  isSaved?: boolean;
  onToggleSaved?: () => void;
  channelLabel?: string;
  onJumpToChannel?: () => void;
  onRemindMe?: () => void;
};

export function MessageItem(props: MessageItemProps) {
  const pluginRegistry = usePluginRegistryState();
  const workItemCreation = useWorkItemCreation();
  const {
    post,
    isMe,
    author,
    status,
    reactions,
    currentUserId,
    files,
    token,
    onToggleReaction,
    onEdit,
    onDelete,
    onOpenThread,
    domAnchorId,
    compact,
    hideThreadAction,
    isSaved,
    onToggleSaved,
    channelLabel,
    onJumpToChannel,
    onRemindMe,
  } = props;
  const [editing, setEditing] = useState(false);
  const [editSaving, setEditSaving] = useState(false);
  const [editError, setEditError] = useState("");
  const [draft, setDraft] = useState(post.message);
  const [pickerOpen, setPickerOpen] = useState(false);
  const [moreMenuAnchor, setMoreMenuAnchor] = useState<HTMLElement | null>(null);
  const moreMenuID = `message-actions-${useId()}`;
  const editSavingRef = useRef(false);
  const editRef = useRef<HTMLTextAreaElement>(null);
  const editMentions = useMentionAutocomplete({
    token,
    channelID: post.channel_id,
    value: draft,
    setValue: setDraft,
    textareaRef: editRef,
  });
  const editDraft = useDraft(
    editing && currentUserId ? `moyro:draft:edit:${currentUserId}:${post.id}` : null,
    draft,
    setDraft,
    post.message,
  );

  const groupedReactions = useMemo(() => {
    const grouped: Record<string, Reaction[]> = {};
    reactions.forEach((reaction) => { (grouped[reaction.emoji_name] ||= []).push(reaction); });
    return grouped;
  }, [reactions]);

  const authorName = author?.username ?? (isMe ? "나" : post.user_id.slice(0, 8));
  const edited = post.update_at > post.create_at;
  const postType = (post as unknown as { type?: string }).type
    ?? (typeof post.props?.type === "string" ? post.props.type : "");
  const pluginPostType = pluginRegistry.postTypeComponents.find((entry) => entry.postType === postType);

  function cancelEdit() {
    editDraft.clearSaved();
    setEditing(false);
    setDraft(post.message);
  }

  function runMoreAction(action: () => void) {
    setMoreMenuAnchor(null);
    action();
  }

  return (
    <div
      id={domAnchorId}
      className={`msg workspace-message-item ${isMe ? "msg-me" : ""} ${compact ? "msg-compact" : ""}`}
      role="group"
      aria-label={`${authorName}의 메시지`}
      tabIndex={-1}
    >
      <div className="msg-meta">
        <WorkspaceAvatar
          token={token}
          id={post.user_id}
          name={author?.username ?? ""}
          status={status}
          size={20}
          picture={author?.picture}
          updateAt={author?.update_at}
        />
        <span className="msg-author">{authorName}</span>
        <time className="msg-time" dateTime={new Date(post.create_at).toISOString()}>
          {formatMessageTime(post.create_at)}
        </time>
        {edited && <span className="msg-edited">(편집됨)</span>}
        {post.is_pinned && <PushPinRounded className="msg-pinned" fontSize="inherit" aria-label="고정된 메시지" />}
        {channelLabel && (
          <button
            type="button"
            className="msg-channel-chip"
            onClick={onJumpToChannel}
            title="이 채널로 이동"
            aria-label={`#${channelLabel} 채널로 이동`}
          >
            #{channelLabel}
          </button>
        )}
      </div>

      {editing ? (
        <form
          onSubmit={(event) => {
            event.preventDefault();
            if (!draft.trim() || editSavingRef.current) return;
            editSavingRef.current = true;
            setEditSaving(true);
            setEditError("");
            void onEdit(post.id, draft.trim()).then((saved) => {
              if (!saved) {
                setEditError("메시지를 수정하지 못했습니다. 편집 내용은 유지됩니다.");
                return;
              }
              editDraft.clearSaved();
              setEditing(false);
            }).catch((error: unknown) => {
              setEditError(error instanceof Error ? error.message : "메시지 수정에 실패했습니다.");
            }).finally(() => {
              editSavingRef.current = false;
              setEditSaving(false);
            });
          }}
        >
          <div className="mention-picker-host">
            <textarea
              ref={editRef}
              className="composer-input"
              aria-label="메시지 편집"
              value={draft}
              onChange={(event) => {
                setDraft(event.target.value);
                setEditError("");
                editMentions.onChange(event);
              }}
              rows={2}
              autoFocus
              onKeyDown={(event) => {
                if (editMentions.handleKeyDown(event)) return;
                if (event.key === "Escape" && !editSaving) cancelEdit();
              }}
            />
            {editMentions.render()}
          </div>
          <div className="message-edit-actions">
            <button type="submit" className="btn-primary" disabled={editSaving}>
              {editSaving ? "저장 중…" : "저장"}
            </button>
            <button type="button" className="btn-ghost" onClick={cancelEdit} disabled={editSaving}>취소</button>
          </div>
          {editError && <div className="message-edit-error" role="alert">{editError}</div>}
        </form>
      ) : (
        <>
          {pluginPostType ? (
            <PluginSurface
              component={pluginPostType.component}
              componentProps={{ post }}
              label={`${pluginPostType.pluginId} post`}
            />
          ) : post.message && (
            <MessageBody source={post.message} token={token} linkMetadata={post.link_metadata} />
          )}
          {files.length > 0 && (
            <div className="msg-files">
              {files.map((file) => <MessageFileChip key={file.id} file={file} token={token} />)}
            </div>
          )}
        </>
      )}

      {Object.keys(groupedReactions).length > 0 && (
        <div className="reactions">
          {Object.entries(groupedReactions).map(([emoji, matchingReactions]) => {
            const mine = matchingReactions.some((reaction) => reaction.user_id === currentUserId);
            const custom = customEmojiByName(emoji);
            return (
              <button
                key={emoji}
                type="button"
                className={`reaction-chip ${mine ? "reaction-mine" : ""}`}
                onClick={() => onToggleReaction(emoji)}
                title={matchingReactions.map((reaction) => reaction.user_id).join(", ")}
                aria-label={`${emoji} 리액션 ${matchingReactions.length}개${mine ? ", 내가 추가함" : ""}`}
              >
                {custom ? (
                  <AuthenticatedImage
                    token={token}
                    path={api.emojiImagePath(custom.id)}
                    className="emoji-img"
                    alt={emoji}
                  />
                ) : (
                  <span>{emojiCharacter(emoji)}</span>
                )}
                <span className="reaction-count">{matchingReactions.length}</span>
              </button>
            );
          })}
        </div>
      )}

      {!editing && !compact && (
        <div className="msg-actions message-actions-toolbar" aria-label="메시지 작업">
          <button
            type="button"
            className="action-btn message-action-button message-action-primary"
            onClick={() => setPickerOpen((open) => !open)}
            title="리액션"
            aria-label="리액션 추가"
            aria-expanded={pickerOpen}
          >
            <AddReactionRounded fontSize="inherit" aria-hidden />
          </button>
          {!hideThreadAction && onOpenThread && (
            <button
              type="button"
              className="action-btn message-action-button message-action-primary"
              onClick={() => onOpenThread(post.root_id || post.id)}
              title="스레드 열기"
              aria-label="스레드 열기"
            >
              <ChatBubbleOutlineRounded fontSize="inherit" aria-hidden />
            </button>
          )}
          {onToggleSaved && (
            <button
              type="button"
              className={`action-btn message-action-button message-action-primary ${isSaved ? "action-saved" : ""}`}
              onClick={onToggleSaved}
              title={isSaved ? "저장 해제" : "저장"}
              aria-label={isSaved ? "메시지 저장 해제" : "메시지 저장"}
            >
              {isSaved
                ? <StarRounded fontSize="inherit" aria-hidden />
                : <StarBorderRounded fontSize="inherit" aria-hidden />}
            </button>
          )}
          <button
            type="button"
            className="action-btn message-action-button message-action-more"
            onClick={(event) => {
              setPickerOpen(false);
              setMoreMenuAnchor((anchor) => anchor ? null : event.currentTarget);
            }}
            title="더보기"
            aria-label="메시지 작업 더보기"
            aria-haspopup="menu"
            aria-controls={moreMenuAnchor ? moreMenuID : undefined}
            aria-expanded={Boolean(moreMenuAnchor)}
          >
            <MoreHorizRounded fontSize="inherit" aria-hidden />
          </button>
        </div>
      )}

      {!editing && !compact && (
        <Menu
          id={moreMenuID}
          anchorEl={moreMenuAnchor}
          open={Boolean(moreMenuAnchor)}
          onClose={() => setMoreMenuAnchor(null)}
          slotProps={{
            list: {
              "aria-label": "메시지 작업 더보기",
              className: "message-action-menu",
            },
          }}
        >
          <MenuItem
            className="message-action-menu-item"
            onClick={() => runMoreAction(() => setPickerOpen(true))}
          >
            <AddReactionRounded fontSize="small" aria-hidden />
            <span>리액션 추가</span>
          </MenuItem>
          {!hideThreadAction && onOpenThread && (
            <MenuItem
              className="message-action-menu-item"
              onClick={() => runMoreAction(() => onOpenThread(post.root_id || post.id))}
            >
              <ChatBubbleOutlineRounded fontSize="small" aria-hidden />
              <span>스레드 열기</span>
            </MenuItem>
          )}
          {onToggleSaved && (
            <MenuItem
              className="message-action-menu-item"
              onClick={() => runMoreAction(onToggleSaved)}
            >
              {isSaved
                ? <StarRounded fontSize="small" aria-hidden />
                : <StarBorderRounded fontSize="small" aria-hidden />}
              <span>{isSaved ? "저장 해제" : "저장"}</span>
            </MenuItem>
          )}
          {onRemindMe && (
            <MenuItem
              className="message-action-menu-item"
              onClick={() => runMoreAction(onRemindMe)}
            >
              <NotificationsNoneRounded fontSize="small" aria-hidden />
              <span>나중에 알림</span>
            </MenuItem>
          )}
          {workItemCreation.available && (
            <MenuItem
              className="message-action-menu-item"
              onClick={() => runMoreAction(() => workItemCreation.open(post, "task"))}
            >
              <AssignmentTurnedInRounded fontSize="small" aria-hidden />
              <span>작업으로 만들기</span>
            </MenuItem>
          )}
          {workItemCreation.available && (
            <MenuItem
              className="message-action-menu-item"
              onClick={() => runMoreAction(() => workItemCreation.open(post, "decision"))}
            >
              <GavelRounded fontSize="small" aria-hidden />
              <span>결정으로 기록</span>
            </MenuItem>
          )}
          {isMe && (
            <MenuItem
              className="message-action-menu-item"
              onClick={() => runMoreAction(() => { setEditError(""); setEditing(true); })}
            >
              <EditOutlined fontSize="small" aria-hidden />
              <span>편집</span>
            </MenuItem>
          )}
          {isMe && (
            <MenuItem
              className="message-action-menu-item message-action-menu-item-danger"
              onClick={() => runMoreAction(() => onDelete(post.id))}
            >
              <DeleteOutlineRounded fontSize="small" aria-hidden />
              <span>삭제</span>
            </MenuItem>
          )}
        </Menu>
      )}

      {!editing && !compact && pickerOpen && (
        <div className="message-reaction-picker" role="dialog" aria-label="리액션 선택">
          <EmojiPicker
            token={token}
            quick={QUICK_EMOJIS}
            onPick={(name) => { onToggleReaction(name); setPickerOpen(false); }}
            onClose={() => setPickerOpen(false)}
          />
        </div>
      )}

      {!editing && compact && onToggleSaved && (
        <div className="msg-actions message-actions-persistent" aria-label="메시지 작업">
          <button
            type="button"
            className={`action-btn message-action-button ${isSaved ? "action-saved" : ""}`}
            onClick={onToggleSaved}
            title={isSaved ? "저장 해제" : "저장"}
            aria-label={isSaved ? "메시지 저장 해제" : "메시지 저장"}
          >
            {isSaved
              ? <StarRounded fontSize="inherit" aria-hidden />
              : <StarBorderRounded fontSize="inherit" aria-hidden />}
          </button>
        </div>
      )}
    </div>
  );
}

export function MessageFileChip({ file, token }: { file: FileInfo; token: string }) {
  const [lightboxOpen, setLightboxOpen] = useState(false);
  const [downloading, setDownloading] = useState(false);
  const [downloadFailed, setDownloadFailed] = useState(false);
  const filePath = api.fileDownloadPath(file.id);
  const isImage = file.mime_type?.startsWith("image/");

  if (isImage) {
    const thumbnailPath = file.has_thumbnail ? api.fileThumbnailPath(file.id) : filePath;
    return (
      <>
        <button
          type="button"
          className="file-image"
          onClick={() => setLightboxOpen(true)}
          aria-label={`이미지 확대: ${file.name}`}
        >
          <AuthenticatedImage token={token} path={thumbnailPath} alt={file.name} loading="lazy" />
        </button>
        {lightboxOpen && (
          <Lightbox token={token} path={filePath} alt={file.name} onClose={() => setLightboxOpen(false)} />
        )}
      </>
    );
  }

  async function download() {
    if (downloading) return;
    setDownloading(true);
    setDownloadFailed(false);
    try {
      await downloadAuthenticatedMedia(token, filePath, file.name);
    } catch {
      setDownloadFailed(true);
    } finally {
      setDownloading(false);
    }
  }

  const stateLabel = downloading
    ? "받는 중…"
    : downloadFailed
      ? "실패 — 다시 시도"
      : humanFileSize(file.size);

  return (
    <button
      type="button"
      className="file-chip"
      onClick={() => void download()}
      disabled={downloading}
      title={downloadFailed ? "파일을 다운로드하지 못했습니다." : undefined}
      aria-label={`${file.name} 다운로드${stateLabel ? `, ${stateLabel}` : ""}`}
    >
      <AttachFileRounded className="file-icon" fontSize="inherit" aria-hidden />
      <span className="file-name">{file.name}</span>
      <span className="file-size" aria-live="polite">{stateLabel}</span>
    </button>
  );
}
