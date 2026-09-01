import { moyroRequest } from "./transport";

export type DocumentRecord = {
  id: string;
  title: string;
  body: string;
  created_by: string;
  team_id?: string;
  channel_id: string;
  source_thread_id: string;
  /** Opaque positive revision of the complete source thread, not a timestamp. */
  source_cursor_at: number;
  revision: number;
  create_at: number;
  update_at: number;
  delete_at: number;
  stale: boolean;
};

export type DocumentSourcePost = {
  id: string;
  channel_id: string;
  user_id: string;
  username: string;
  root_id: string;
  message: string;
  create_at: number;
  update_at: number;
};

export type DocumentSource = {
  team_id?: string;
  channel_id: string;
  thread_id: string;
  /** Opaque positive revision of posts and content in this source snapshot. */
  cursor_at: number;
  posts: DocumentSourcePost[];
};

export type CreateDocumentInput = {
  title: string;
  body: string;
  source_post_id: string;
  source_cursor_at: number;
  idempotency_key: string;
};

export type PatchDocumentInput = {
  title?: string;
  body?: string;
  source_cursor_at?: number;
  expected_revision: number;
};

export const documentsApi = {
  list: (token: string, limit = 50, signal?: AbortSignal) => {
    const query = new URLSearchParams({ limit: String(limit) });
    return moyroRequest<DocumentRecord[]>(token, `/me/documents?${query}`, { signal });
  },
  get: (token: string, id: string, signal?: AbortSignal) =>
    moyroRequest<DocumentRecord>(token, `/me/documents/${encodeURIComponent(id)}`, { signal }),
  source: (token: string, postID: string, signal?: AbortSignal) =>
    moyroRequest<DocumentSource>(token, `/me/document-sources/${encodeURIComponent(postID)}`, { signal }),
  create: (token: string, input: CreateDocumentInput, signal?: AbortSignal) =>
    moyroRequest<{ document: DocumentRecord; replayed: boolean }>(token, "/me/documents", {
      method: "POST",
      headers: { "Idempotency-Key": input.idempotency_key },
      body: input,
      signal,
    }),
  patch: (token: string, id: string, input: PatchDocumentInput, signal?: AbortSignal) =>
    moyroRequest<DocumentRecord>(token, `/me/documents/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: input,
      signal,
    }),
  remove: (token: string, id: string, revision: number, signal?: AbortSignal) => {
    const query = new URLSearchParams({ revision: String(revision) });
    return moyroRequest<void>(token, `/me/documents/${encodeURIComponent(id)}?${query}`, {
      method: "DELETE",
      signal,
    });
  },
};
