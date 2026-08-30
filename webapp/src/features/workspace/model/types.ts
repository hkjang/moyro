import type {
  FileInfo,
  Reaction,
  User,
  UserStatusValue,
} from "@/api/client";

export type UnreadEntry = { msg: number; mention: number };
export type UsersMap = Record<string, User>;
export type StatusMap = Record<string, UserStatusValue>;
export type ReactionMap = Record<string, Reaction[]>;
export type FilesMap = Record<string, FileInfo>;

export type ReminderToast = {
  id: string;
  postId: string;
  channelId: string;
  excerpt: string;
};
