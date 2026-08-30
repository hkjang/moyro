import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  api,
  compatApi,
  customProfileApi,
  notifyApi,
  type Channel,
  type CustomProfileField,
  type CustomProfileValues,
  type SessionRow,
  type User,
  type UserNotifyProps,
  type UserStatusValue,
} from "@/api/client";
import { BrandMark } from "@/components/brand/BrandMark";
import { useEscClose } from "@/components/shared";
import { WorkspaceAvatar } from "@/features/workspace/sidebar/WorkspaceAvatar";
import type { UsersMap } from "@/features/workspace/model/types";

type ChannelDiscoverModalProps = {
  token: string;
  teamId: string;
  onClose: () => void;
  onJoined: (channelId: string) => void;
};

export function ChannelDiscoverModal({ token, teamId, onClose, onJoined }: ChannelDiscoverModalProps) {
  useEscClose(true, onClose);
  const [q, setQ] = useState("");
  const [rows, setRows] = useState<Channel[]>([]);
  const [loading, setLoading] = useState(false);
  const [offset, setOffset] = useState(0);
  const [hasMore, setHasMore] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [joining, setJoining] = useState<string | null>(null);

  const doFetch = useCallback(async (reset: boolean) => {
    setLoading(true);
    setErr(null);
    try {
      const nextOffset = reset ? 0 : offset;
      const list = await api.discoverChannels(token, teamId, {
        q: q.trim(),
        limit: 20,
        offset: nextOffset,
      });
      if (reset) setRows(list);
      else setRows((prev) => [...prev, ...list]);
      setHasMore((list?.length ?? 0) >= 20);
      setOffset(nextOffset + (list?.length ?? 0));
    } catch (e) {
      setErr(e instanceof Error ? e.message : "채널 탐색 실패");
    } finally {
      setLoading(false);
    }
  }, [token, teamId, q, offset]);

  // Debounce the initial + query-change load so typing doesn't spam the API.
  useEffect(() => {
    const t = setTimeout(() => { doFetch(true); }, q ? 180 : 0);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [q]);

  async function onJoin(ch: Channel) {
    if (joining) return;
    setJoining(ch.id);
    try {
      await api.joinChannel(token, ch.id);
      setRows((prev) => prev.filter((c) => c.id !== ch.id));
      onJoined(ch.id);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "채널 참여 실패");
    } finally {
      setJoining(null);
    }
  }

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div
        className="modal-card channel-discover-modal"
        onClick={(e) => e.stopPropagation()}
      >
        <h3 style={{ margin: "0 0 12px" }}>🔍 채널 탐색</h3>
        <input
          className="field-input"
          autoFocus
          placeholder="이름 또는 표시 이름으로 검색"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          style={{ marginBottom: 10 }}
        />
        {err && <div className="login-error">{err}</div>}
        <div className="discover-list">
          {rows.length === 0 && !loading && (
            <div className="chat-empty" style={{ padding: 16 }}>
              {q ? "검색 결과가 없습니다." : "참여할 수 있는 공개 채널이 없습니다."}
            </div>
          )}
          {rows.map((c) => (
            <div key={c.id} className="discover-row">
              <div className="discover-row-main">
                <div className="discover-row-title">
                  <span className="channel-hash">#</span>
                  {c.display_name}
                </div>
                <div className="discover-row-name">{c.name}</div>
                {c.purpose && <div className="discover-row-purpose">{c.purpose}</div>}
              </div>
              <button
                type="button"
                className="btn-primary"
                style={{ width: "auto", padding: "0 14px", height: 32 }}
                disabled={joining === c.id}
                onClick={() => onJoin(c)}
              >
                {joining === c.id ? "참여 중…" : "참여"}
              </button>
            </div>
          ))}
        </div>
        <div style={{ display: "flex", justifyContent: "space-between", marginTop: 10 }}>
          <button type="button" className="btn-ghost" onClick={onClose}>닫기</button>
          {hasMore && (
            <button
              type="button"
              className="btn-ghost"
              disabled={loading}
              onClick={() => doFetch(false)}
            >
              {loading ? "불러오는 중…" : "더 보기"}
            </button>
          )}
        </div>
      </div>
    </div>
  );
}

export function StartDirectModal({
  token, currentUserId, onClose, onPick,
}: {
  token: string;
  currentUserId: string;
  onClose: () => void;
  onPick: (userId: string) => void;
}) {
  useEscClose(true, onClose);
  const [q, setQ] = useState("");
  const [results, setResults] = useState<User[]>([]);

  useEffect(() => {
    const t = setTimeout(async () => {
      try {
        if (q.trim()) {
          const list = await api.searchUsers(token, q.trim(), 20);
          setResults(list.filter((u) => u.id !== currentUserId));
        } else {
          const list = await api.listUsers(token, 0, 20);
          setResults(list.filter((u) => u.id !== currentUserId));
        }
      } catch { /* ignore */ }
    }, 200);
    return () => clearTimeout(t);
  }, [q, token, currentUserId]);

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal-card" onClick={(e) => e.stopPropagation()}>
        <h3 style={{ margin: "0 0 12px" }}>새 다이렉트 메시지</h3>
        <input
          className="field-input"
          autoFocus
          placeholder="사용자 검색…"
          value={q}
          onChange={(e) => setQ(e.target.value)}
        />
        <div className="user-picker">
          {results.length === 0 ? (
            <div className="chat-empty" style={{ padding: 16 }}>결과 없음</div>
          ) : results.map((u) => (
            <button key={u.id} className="item" onClick={() => onPick(u.id)}>
              <WorkspaceAvatar token={token} id={u.id} name={u.username} size={22} picture={u.picture} updateAt={u.update_at} />
              <span style={{ marginLeft: 2 }}>{u.username}</span>
              <span style={{ color: "var(--muted)", fontSize: 13, marginLeft: "auto" }}>{u.email}</span>
            </button>
          ))}
        </div>
        <div style={{ display: "flex", justifyContent: "flex-end", marginTop: 12 }}>
          <button className="btn-ghost" style={{ width: "auto", padding: "0 14px", height: 34 }} onClick={onClose}>닫기</button>
        </div>
      </div>
    </div>
  );
}

// Phase 16 — session management drawer. Lists the user's live sessions
// (IP-ish device_id + expiry) with revoke buttons per-row and a "kill all
// other devices" catch-all at the bottom. The current row is tagged by
// the server via `is_current` (matches the JWT behind the request); we
// don't ship the bearer token to the client for comparison.
export function SessionManagerModal({
  sessions,
  loading,
  onRevoke,
  onRevokeOthers,
  onClose,
}: {
  sessions: SessionRow[];
  loading: boolean;
  onRevoke: (sessionId: string) => void;
  onRevokeOthers: () => void;
  onClose: () => void;
}) {
  useEscClose(true, onClose);
  const others = sessions.filter((s) => !s.is_current).length;
  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal-card" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 520 }}>
        <header className="integrations-header">
          <h3 style={{ margin: 0 }}>세션 관리</h3>
          <button type="button" className="action-btn" onClick={onClose} title="닫기">✕</button>
        </header>
        <div style={{ padding: "4px 0 10px", color: "var(--muted)", fontSize: 13 }}>
          이 계정으로 로그인한 모든 기기의 세션입니다. 의심스러운 세션이 있다면 즉시 종료하세요.
        </div>
        {loading ? (
          <div className="chat-empty" style={{ padding: 14 }}>불러오는 중…</div>
        ) : sessions.length === 0 ? (
          <div className="chat-empty" style={{ padding: 14 }}>활성 세션이 없습니다.</div>
        ) : (
          <ul className="integrations-list">
            {sessions.map((s) => (
              <li key={s.id} className="integrations-row">
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div style={{ fontWeight: 600 }}>
                    {s.is_current ? "이 기기" : (s.device_id || "알 수 없는 기기")}
                  </div>
                  <div style={{ color: "var(--muted)", fontSize: 13 }}>
                    생성 {new Date(s.create_at).toLocaleString()}
                    {" · 만료 "}
                    {new Date(s.expires_at).toLocaleString()}
                  </div>
                </div>
                <button
                  type="button"
                  className="btn-ghost"
                  style={{ width: "auto", padding: "0 10px", height: 30, color: "var(--danger)" }}
                  onClick={() => onRevoke(s.id)}
                >종료</button>
              </li>
            ))}
          </ul>
        )}
        <div style={{ marginTop: 12, display: "flex", justifyContent: "flex-end" }}>
          <button
            type="button"
            className="btn-ghost"
            style={{ width: "auto", padding: "0 14px", height: 36, color: "var(--danger)" }}
            onClick={onRevokeOthers}
            disabled={others === 0}
            title={others === 0 ? "다른 기기 세션이 없습니다" : ""}
          >
            다른 모든 기기 로그아웃{others > 0 ? ` (${others})` : ""}
          </button>
        </div>
      </div>
    </div>
  );
}

// ---- Phase 21: Quick Switcher (Cmd+K) ----
//
// Mattermost-style keyboard switcher. Two parallel autocomplete sources
// (channels in the current team + users globally) merged into one selectable
// list. Empty input shows the user's most recent channels so the modal is
// useful even before typing.
//
// Implementation details:
//   - 120ms debounce on the input. Keeps requests sane while still feeling
//     instant under typical typing.
//   - Sequence-guarded: only the latest in-flight result lands in state.
//   - Arrow keys cycle, Enter selects, Esc closes (via useEscClose).
//   - Already-known users from the parent's `users` map render with avatars
//     even before the autocomplete result populates.
type QuickSwitcherEntry =
  | { kind: "channel"; channel: Channel }
  | { kind: "user"; user: User };

export function QuickSwitcherModal({
  token,
  teamId,
  channels,
  users,
  meId,
  onClose,
  onPickChannel,
  onPickUser,
}: {
  token: string;
  teamId: string | null;
  channels: Channel[];
  users: UsersMap;
  meId: string;
  onClose: () => void;
  onPickChannel: (channelId: string) => void;
  onPickUser: (user: User) => void;
}) {
  useEscClose(true, onClose);
  const [query, setQuery] = useState("");
  const [channelHits, setChannelHits] = useState<Channel[]>([]);
  const [userHits, setUserHits] = useState<User[]>([]);
  const [activeIdx, setActiveIdx] = useState(0);
  const inputRef = useRef<HTMLInputElement | null>(null);
  const reqSeqRef = useRef(0);

  // Initial focus + initial channel suggestions (recent joined channels).
  useEffect(() => {
    inputRef.current?.focus();
  }, []);

  // Debounced fetch. Empty query falls back to the local channel list so
  // the modal isn't blank on open.
  useEffect(() => {
    const term = query.trim();
    if (!term) {
      // Up to 8 most recent channels the user belongs to.
      setChannelHits(channels.slice(0, 8));
      setUserHits([]);
      setActiveIdx(0);
      return;
    }
    const seq = ++reqSeqRef.current;
    const handle = setTimeout(async () => {
      try {
        const [chs, ures] = await Promise.all([
          teamId ? compatApi.autocompleteChannels(token, teamId, term).catch(() => []) : Promise.resolve([] as Channel[]),
          compatApi.autocompleteUsers(token, term, 10).catch(() => ({ users: [] as User[], out_of_channel: [] as User[] })),
        ]);
        if (reqSeqRef.current !== seq) return;
        setChannelHits(chs);
        setUserHits(ures.users.filter((u) => u.id !== meId));
        setActiveIdx(0);
      } catch {
        if (reqSeqRef.current !== seq) return;
        setChannelHits([]);
        setUserHits([]);
      }
    }, 120);
    return () => clearTimeout(handle);
  }, [query, token, teamId, channels, meId]);

  const entries = useMemo<QuickSwitcherEntry[]>(() => {
    return [
      ...channelHits.map((c) => ({ kind: "channel" as const, channel: c })),
      ...userHits.map((u) => ({ kind: "user" as const, user: u })),
    ];
  }, [channelHits, userHits]);

  // Clamp the cursor whenever the result list shrinks under it.
  useEffect(() => {
    if (activeIdx >= entries.length) setActiveIdx(Math.max(0, entries.length - 1));
  }, [entries.length, activeIdx]);

  const choose = useCallback(
    (e: QuickSwitcherEntry) => {
      if (e.kind === "channel") onPickChannel(e.channel.id);
      else onPickUser(e.user);
    },
    [onPickChannel, onPickUser],
  );

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div
        className="modal-card quick-switcher"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-label="빠른 이동"
      >
        <input
          ref={inputRef}
          className="quick-switcher-input"
          type="text"
          placeholder="채널이나 사용자를 입력하세요…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "ArrowDown") {
              e.preventDefault();
              setActiveIdx((i) => Math.min(entries.length - 1, i + 1));
            } else if (e.key === "ArrowUp") {
              e.preventDefault();
              setActiveIdx((i) => Math.max(0, i - 1));
            } else if (e.key === "Enter") {
              e.preventDefault();
              const entry = entries[activeIdx];
              if (entry) choose(entry);
            }
          }}
        />
        <ul className="quick-switcher-list" role="listbox">
          {entries.length === 0 && (
            <li className="quick-switcher-empty">결과 없음</li>
          )}
          {entries.map((entry, idx) => {
            const active = idx === activeIdx;
            if (entry.kind === "channel") {
              const c = entry.channel;
              const symbol = c.type === "P" ? "🔒" : c.type === "D" ? "👤" : c.type === "G" ? "👥" : "#";
              return (
                <li
                  key={"ch-" + c.id}
                  className={"quick-switcher-row" + (active ? " active" : "")}
                  role="option"
                  aria-selected={active}
                  onMouseEnter={() => setActiveIdx(idx)}
                  onMouseDown={(ev) => { ev.preventDefault(); choose(entry); }}
                >
                  <span className="quick-switcher-icon">{symbol}</span>
                  <span className="quick-switcher-name">{c.display_name || c.name}</span>
                  <span className="quick-switcher-sub">채널</span>
                </li>
              );
            }
            const u = entry.user;
            const cached = users[u.id];
            const display = u.username + (cached?.username && cached.username !== u.username ? ` (${cached.username})` : "");
            return (
              <li
                key={"u-" + u.id}
                className={"quick-switcher-row" + (active ? " active" : "")}
                role="option"
                aria-selected={active}
                onMouseEnter={() => setActiveIdx(idx)}
                onMouseDown={(ev) => { ev.preventDefault(); choose(entry); }}
              >
                <span className="quick-switcher-icon">@</span>
                <span className="quick-switcher-name">{display}</span>
                <span className="quick-switcher-sub">DM</span>
              </li>
            );
          })}
        </ul>
        <div className="quick-switcher-hint">
          ↑↓ 이동 · Enter 선택 · Esc 닫기
        </div>
      </div>
    </div>
  );
}

// Phase 22 — user-level notify_props panel. Persists through PUT
// /users/me/notify_props which writes the full map atomically. Each row in
// the form is a string→string entry — Mattermost's contract intentionally
// never types the values past TEXT so future provider plugins can extend
// the map without a migration. We surface the four most actioned keys with
// dropdowns and let everything else round-trip untouched.
export function NotifyPrefsModal({ token, onClose }: { token: string; onClose: () => void }) {
  const [props, setProps] = useState<UserNotifyProps>({});
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  useEscClose(true, onClose);
  useEffect(() => {
    let cancelled = false;
    notifyApi
      .get(token)
      .then((p) => { if (!cancelled) setProps(p ?? {}); })
      .catch((e) => { if (!cancelled) setError((e as Error).message); })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [token]);
  const update = (key: string, value: string) =>
    setProps((prev) => ({ ...prev, [key]: value }));
  const save = async () => {
    setSaving(true);
    setError(null);
    try {
      const next = await notifyApi.put(token, props);
      setProps(next ?? props);
      onClose();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };
  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()} role="dialog" aria-modal>
        <h3 style={{ margin: "0 0 16px" }}>알림 설정</h3>
        {loading ? (
          <div style={{ color: "var(--muted)" }}>불러오는 중…</div>
        ) : (
          <div style={{ display: "grid", gap: 12 }}>
            <label className="notify-row">
              <span>데스크톱 알림</span>
              <select value={props.desktop ?? "mention"} onChange={(e) => update("desktop", e.target.value)}>
                <option value="all">모든 메시지</option>
                <option value="mention">멘션 + DM만</option>
                <option value="none">받지 않기</option>
              </select>
            </label>
            <label className="notify-row">
              <span>알림음</span>
              <select value={props.desktop_sound ?? "true"} onChange={(e) => update("desktop_sound", e.target.value)}>
                <option value="true">사용</option>
                <option value="false">사용 안 함</option>
              </select>
            </label>
            <label className="notify-row">
              <span>이메일 알림</span>
              <select value={props.email ?? "true"} onChange={(e) => update("email", e.target.value)}>
                <option value="true">사용</option>
                <option value="false">사용 안 함</option>
              </select>
            </label>
            <label className="notify-row">
              <span>이름 멘션 강조</span>
              <select value={props.first_name ?? "false"} onChange={(e) => update("first_name", e.target.value)}>
                <option value="true">사용</option>
                <option value="false">사용 안 함</option>
              </select>
            </label>
            <label className="notify-row">
              <span>채널 전체 호출 (@channel)</span>
              <select value={props.channel ?? "true"} onChange={(e) => update("channel", e.target.value)}>
                <option value="true">받기</option>
                <option value="false">무시</option>
              </select>
            </label>
            <label className="notify-row">
              <span>강조 키워드 (쉼표 구분)</span>
              <input
                type="text"
                value={props.mention_keys ?? ""}
                onChange={(e) => update("mention_keys", e.target.value)}
                placeholder="배포, 긴급"
                style={{ flex: 1 }}
              />
            </label>
          </div>
        )}
        {error && (
          <div style={{ color: "var(--danger)", marginTop: 12, fontSize: 13 }}>{error}</div>
        )}
        <div style={{ display: "flex", justifyContent: "flex-end", gap: 8, marginTop: 16 }}>
          <button type="button" className="btn-ghost" onClick={onClose}>취소</button>
          <button
            type="button"
            className="btn-primary"
            onClick={save}
            disabled={loading || saving}
          >
            {saving ? "저장 중…" : "저장"}
          </button>
        </div>
      </div>
    </div>
  );
}

// Phase 33 — Custom profile attributes drawer. Renders the admin-curated
// field defs and the caller's per-field values. Two views side-by-side:
//
//   Left:  the user's own value form (always visible).
//   Right: the admin field-management form (only visible to system_admins).
//
// Field defs are global so a non-admin user only sees the value form. The
// modal lazy-loads on open and refetches both fields and values together
// so a freshly-added admin field shows up immediately for users who happen
// to have the modal open.
export function CustomProfileFieldsModal({
  token,
  isAdmin,
  onClose,
}: {
  token: string;
  isAdmin: boolean;
  onClose: () => void;
}) {
  const [fields, setFields] = useState<CustomProfileField[]>([]);
  const [values, setValues] = useState<CustomProfileValues>({});
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Admin-mode toggle: when true, render the field-defs editor instead of
  // (or rather, alongside) the value form. Hidden entirely for non-admins.
  const [adminMode, setAdminMode] = useState(false);
  // New-field form. Stored separately so the user can type a name + pick a
  // type before the row is committed.
  const [newName, setNewName] = useState("");
  const [newType, setNewType] = useState<string>("text");
  useEscClose(true, onClose);

  const reload = useCallback(async () => {
    setLoading(true);
    try {
      const [fs, vs] = await Promise.all([
        customProfileApi.listFields(token),
        customProfileApi.getUserValues(token),
      ]);
      setFields(Array.isArray(fs) ? fs : []);
      setValues(vs ?? {});
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [token]);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const [fs, vs] = await Promise.all([
          customProfileApi.listFields(token),
          customProfileApi.getUserValues(token),
        ]);
        if (cancelled) return;
        setFields(Array.isArray(fs) ? fs : []);
        setValues(vs ?? {});
      } catch (e) {
        if (!cancelled) setError((e as Error).message);
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => { cancelled = true; };
  }, [token]);

  const updateValue = (fieldId: string, raw: unknown) =>
    setValues((prev) => ({ ...prev, [fieldId]: raw }));

  const save = async () => {
    setSaving(true);
    setError(null);
    try {
      // Only PATCH the keys that map to current field defs — drops orphan
      // entries from a since-deleted field so the next reload starts clean.
      const filtered: CustomProfileValues = {};
      for (const f of fields) {
        if (Object.prototype.hasOwnProperty.call(values, f.id)) {
          filtered[f.id] = values[f.id];
        }
      }
      const next = await customProfileApi.patchMyValues(token, filtered);
      setValues(next ?? filtered);
      onClose();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const createField = async () => {
    const name = newName.trim();
    if (!name) return;
    setSaving(true);
    try {
      await customProfileApi.createField(token, { name, type: newType });
      setNewName("");
      setNewType("text");
      await reload();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const deleteField = async (id: string) => {
    setSaving(true);
    try {
      await customProfileApi.deleteField(token, id);
      await reload();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()} role="dialog" aria-modal style={{ maxWidth: 560 }}>
        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", marginBottom: 12 }}>
          <h3 style={{ margin: 0 }}>프로필 항목</h3>
          {isAdmin && (
            <label style={{ display: "flex", alignItems: "center", gap: 6, fontSize: 13 }}>
              <input
                type="checkbox"
                checked={adminMode}
                onChange={(e) => setAdminMode(e.target.checked)}
              />
              필드 관리 (관리자)
            </label>
          )}
        </div>
        {loading ? (
          <div style={{ color: "var(--muted)" }}>불러오는 중…</div>
        ) : adminMode ? (
          <div style={{ display: "grid", gap: 10 }}>
            {fields.length === 0 ? (
              <div style={{ color: "var(--muted)", fontSize: 13 }}>
                정의된 항목이 없습니다. 아래에서 새 항목을 추가하세요.
              </div>
            ) : (
              fields.map((f) => (
                <div key={f.id} style={{ display: "flex", alignItems: "center", gap: 8 }}>
                  <strong style={{ flex: 1 }}>{f.name}</strong>
                  <span style={{ color: "var(--muted)", fontSize: 13 }}>{f.type}</span>
                  <button
                    type="button"
                    className="btn-ghost"
                    style={{ fontSize: 13 }}
                    onClick={() => void deleteField(f.id)}
                    disabled={saving}
                  >
                    삭제
                  </button>
                </div>
              ))
            )}
            <div style={{ marginTop: 12, paddingTop: 12, borderTop: "1px solid var(--border)" }}>
              <div style={{ fontSize: 13, color: "var(--muted)", marginBottom: 6 }}>새 항목 추가</div>
              <div style={{ display: "flex", gap: 6 }}>
                <input
                  type="text"
                  value={newName}
                  onChange={(e) => setNewName(e.target.value)}
                  placeholder="이름 (예: 부서)"
                  style={{ flex: 1 }}
                />
                <select
                  value={newType}
                  onChange={(e) => setNewType(e.target.value)}
                  style={{ width: 110 }}
                >
                  <option value="text">텍스트</option>
                  <option value="url">URL</option>
                  <option value="phone">전화</option>
                  <option value="select">선택</option>
                  <option value="date">날짜</option>
                </select>
                <button
                  type="button"
                  className="btn-primary"
                  onClick={() => void createField()}
                  disabled={saving || newName.trim() === ""}
                >
                  추가
                </button>
              </div>
            </div>
          </div>
        ) : fields.length === 0 ? (
          <div style={{ color: "var(--muted)", fontSize: 13 }}>
            관리자가 정의한 프로필 항목이 없습니다.
          </div>
        ) : (
          <div style={{ display: "grid", gap: 12 }}>
            {fields.map((f) => {
              const cur = values[f.id];
              const asString = typeof cur === "string" ? cur : cur == null ? "" : String(cur);
              const inputType =
                f.type === "url" ? "url" :
                f.type === "phone" ? "tel" :
                f.type === "date" ? "date" :
                "text";
              return (
                <label key={f.id} className="notify-row">
                  <span>{f.name}</span>
                  <input
                    type={inputType}
                    value={asString}
                    onChange={(e) => updateValue(f.id, e.target.value)}
                    style={{ flex: 1 }}
                  />
                </label>
              );
            })}
          </div>
        )}
        {error && (
          <div style={{ color: "var(--danger)", marginTop: 12, fontSize: 13 }}>{error}</div>
        )}
        <div style={{ display: "flex", justifyContent: "flex-end", gap: 8, marginTop: 16 }}>
          <button type="button" className="btn-ghost" onClick={onClose}>닫기</button>
          {!adminMode && (
            <button
              type="button"
              className="btn-primary"
              onClick={() => void save()}
              disabled={loading || saving || fields.length === 0}
            >
              {saving ? "저장 중…" : "저장"}
            </button>
          )}
        </div>
      </div>
    </div>
  );
}

// Phase 34 — Mattermost-v11-style user menu rendered as an overlay so it
// floats above the chat-header. The trigger lives in the chat-header right;
// this component handles the dropdown panel + click-outside backdrop +
// Esc-to-close. Every interactive entry is a real button so keyboard
// navigation (Tab + Enter) works out of the box.
export function UserMenuOverlay(props: {
  username: string;
  email: string;
  status: UserStatusValue;
  onChangeStatus: (next: UserStatusValue) => void;
  theme: "light" | "dark" | "system";
  onChangeTheme: (next: "light" | "dark" | "system") => void;
  digestEnabled: boolean | null;
  digestAvailable: boolean;
  onToggleDigest: (next: boolean) => void;
  uploadingAvatar: boolean;
  onUploadAvatar: () => void;
  onOpenProfileFields: () => void;
  onOpenNotifyPrefs: () => void;
  onOpenSessions: () => void;
  onOpenQuickSwitcher: () => void;
  onOpenPersonalSettings: () => void;
  onOpenMyApprovals: () => void;
  onOpenApprovalReviews: () => void;
  onOpenAdmin: () => void;
  isAdmin: boolean;
  approvalEnabled: boolean;
  version: string;
  buildHash?: string;
  onLogout: () => void;
  onClose: () => void;
}) {
  useEscClose(true, props.onClose);
  const statusLabel: Record<UserStatusValue, string> = {
    online: "온라인",
    away: "자리비움",
    dnd: "방해금지",
    offline: "오프라인",
  };
  return (
    <div className="user-menu-layer" role="presentation">
      {/* Invisible backdrop catches clicks outside the panel and closes
          the menu. Pointer-events on the panel itself stay enabled so
          clicks inside don't propagate to the backdrop. */}
      <button
        type="button"
        className="user-menu-backdrop"
        aria-label="메뉴 닫기"
        onClick={props.onClose}
      />
      <div className="user-menu" role="menu" aria-label="계정 메뉴">
        <div className="user-menu-scroll">
        <div className="user-menu-head">
          <div className="user-menu-name">{props.username || "사용자"}</div>
          {props.email && <div className="user-menu-email">{props.email}</div>}
          <div className="user-menu-current-status" aria-live="polite">
            <span className={`status-dot status-${props.status}`} aria-hidden />
            <span>{statusLabel[props.status]}</span>
          </div>
        </div>

        <div className="user-menu-section">
          <div className="user-menu-section-label">상태 변경</div>
          <div className="user-menu-status-row">
            {(["online", "away", "dnd", "offline"] as UserStatusValue[]).map((s) => (
              <button
                key={s}
                type="button"
                className={`user-menu-status-pill ${props.status === s ? "is-active" : ""} status-${s}`}
                onClick={() => props.onChangeStatus(s)}
                role="menuitemradio"
                aria-checked={props.status === s}
              >
                <span className={`status-dot status-${s}`} aria-hidden />
                <span>{statusLabel[s]}</span>
                {props.status === s && <span className="user-menu-check" aria-hidden>✓</span>}
              </button>
            ))}
          </div>
        </div>

        <div className="user-menu-divider" />

        <button
          type="button"
          className="user-menu-item"
          role="menuitem"
          onClick={props.onOpenPersonalSettings}
        >
          <span className="user-menu-icon">⚙️</span>
          <span className="user-menu-label">
            내 설정
            <small>프로필 · 개인 키 · AI 설정</small>
          </span>
        </button>
        {props.approvalEnabled && (
          <>
            <button
              type="button"
              className="user-menu-item"
              role="menuitem"
              onClick={props.onOpenMyApprovals}
            >
              <span className="user-menu-icon">📋</span>
              <span className="user-menu-label">
                내 승인 요청
                <small>검토 상태와 실행 결과</small>
              </span>
            </button>
            <button
              type="button"
              className="user-menu-item"
              role="menuitem"
              onClick={props.onOpenApprovalReviews}
            >
              <span className="user-menu-icon">✅</span>
              <span className="user-menu-label">
                검토 대기
                <small>승인 · 반려 결정</small>
              </span>
            </button>
          </>
        )}
        {props.isAdmin && (
          <button
            type="button"
            className="user-menu-item"
            role="menuitem"
            onClick={props.onOpenAdmin}
          >
            <span className="user-menu-icon">🛡️</span>
            <span className="user-menu-label">
              서비스 관리
              <small>SSO · AI · 키 정책 · 승인</small>
            </span>
          </button>
        )}

        <div className="user-menu-divider" />

        <button
          type="button"
          className="user-menu-item"
          role="menuitem"
          onClick={props.onUploadAvatar}
          disabled={props.uploadingAvatar}
        >
          <span className="user-menu-icon">🖼️</span>
          <span className="user-menu-label">
            {props.uploadingAvatar ? "업로드 중…" : "프로필 사진 변경"}
            <small>JPG/PNG · 최대 512KB</small>
          </span>
        </button>
        <button
          type="button"
          className="user-menu-item"
          role="menuitem"
          onClick={props.onOpenProfileFields}
        >
          <span className="user-menu-icon">🪪</span>
          <span className="user-menu-label">
            프로필 항목
            <small>부서 · 전화번호 등 사용자 정의 필드</small>
          </span>
        </button>

        <div className="user-menu-divider" />

        <button
          type="button"
          className="user-menu-item"
          role="menuitem"
          onClick={props.onOpenNotifyPrefs}
        >
          <span className="user-menu-icon">🔔</span>
          <span className="user-menu-label">
            알림 설정
            <small>데스크톱 · 멘션 · 이메일</small>
          </span>
        </button>
        <label className="user-menu-row" role="menuitemcheckbox" aria-checked={props.digestEnabled === true}>
          <span className="user-menu-icon">📧</span>
          <span className="user-menu-label">
            이메일 알림 수신
            <small>{props.digestAvailable ? "하루 한 번 놓친 멘션 요약" : "현재 릴리스에서 이메일 요약을 지원하지 않습니다"}</small>
          </span>
          <input
            type="checkbox"
            checked={props.digestEnabled === true}
            disabled={props.digestEnabled === null || !props.digestAvailable}
            onChange={(e) => props.onToggleDigest(e.target.checked)}
          />
        </label>

        <div className="user-menu-divider" />

        <div className="user-menu-row" role="menuitem">
          <span className="user-menu-icon">🎨</span>
          <span className="user-menu-label">테마</span>
          <select
            className="user-menu-select"
            value={props.theme}
            onChange={(e) => props.onChangeTheme(e.target.value as "light" | "dark" | "system")}
            aria-label="테마 변경"
          >
            <option value="system">시스템</option>
            <option value="light">밝게</option>
            <option value="dark">어둡게</option>
          </select>
        </div>

        <div className="user-menu-divider" />

        <button
          type="button"
          className="user-menu-item"
          role="menuitem"
          onClick={props.onOpenQuickSwitcher}
        >
          <span className="user-menu-icon">🔎</span>
          <span className="user-menu-label">
            빠른 이동
            <small>채널 · 사용자 전환 (Ctrl+K)</small>
          </span>
        </button>
        <button
          type="button"
          className="user-menu-item"
          role="menuitem"
          onClick={props.onOpenSessions}
        >
          <span className="user-menu-icon">🔐</span>
          <span className="user-menu-label">
            세션 관리
            <small>로그인된 기기 보기 · 종료</small>
          </span>
        </button>
        </div>

        <div className="user-menu-footer">
          <button
            type="button"
            className="user-menu-item user-menu-item-danger"
            role="menuitem"
            onClick={props.onLogout}
          >
            <span className="user-menu-icon">↩</span>
            <span className="user-menu-label">로그아웃</span>
          </button>
          <div className="user-menu-version" aria-label={`서비스 버전 ${props.version}`}>
            <span className="user-menu-version-brand"><BrandMark size={20} />moyro {props.version}</span>
            {props.buildHash && <span>build {props.buildHash.slice(0, 8)}</span>}
          </div>
        </div>
      </div>
    </div>
  );
}
