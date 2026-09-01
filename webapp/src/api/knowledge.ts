import { moyroRequest } from "./transport";

export type KnowledgeSourceKind = "message" | "document";

export type KnowledgeSource = {
  ref: string;
  kind: KnowledgeSourceKind;
  id: string;
  team_id?: string;
  channel_id: string;
  post_id?: string;
  document_id?: string;
  source_thread_id?: string;
  title?: string;
  excerpt: string;
  author_id: string;
  author_name: string;
  create_at: number;
  update_at: number;
  rank: number;
};

export type KnowledgeSearchResult = {
  sources: KnowledgeSource[];
  total_hits: number;
};

export type KnowledgeSearchInput = {
  query: string;
  team_id: string;
  channel_id?: string;
  limit?: number;
};

export const knowledgeApi = {
  search: (token: string, input: KnowledgeSearchInput, signal?: AbortSignal) =>
    moyroRequest<KnowledgeSearchResult>(token, "/me/knowledge/search", {
      method: "POST",
      body: input,
      signal,
    }),
};
