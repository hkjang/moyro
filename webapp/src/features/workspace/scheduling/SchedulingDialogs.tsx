import { useState } from "react";
import { useEscClose } from "@/components/shared";

type ScheduleModalProps = {
  channelName: string;
  messagePreview: string;
  onCancel: () => void;
  onConfirm: (sendAtMs: number) => Promise<boolean>;
};

export function ScheduleModal({ channelName, messagePreview, onCancel, onConfirm }: ScheduleModalProps) {
  useEscClose(true, onCancel);
  const [custom, setCustom] = useState<string>(() => {
    const d = new Date(Date.now() + 15 * 60 * 1000);
    d.setSeconds(0, 0);
    const pad = (n: number) => n.toString().padStart(2, "0");
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
  });
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  function tomorrow9am(): number {
    const d = new Date();
    d.setDate(d.getDate() + 1);
    d.setHours(9, 0, 0, 0);
    return d.getTime();
  }

  function nextMonday9am(): number {
    const d = new Date();
    const delta = ((1 - d.getDay() + 7) % 7) || 7;
    d.setDate(d.getDate() + delta);
    d.setHours(9, 0, 0, 0);
    return d.getTime();
  }

  async function send(target: number) {
    if (busy) return;
    if (target <= Date.now() - 30_000) {
      setErr("미래 시각을 선택하세요.");
      return;
    }
    setBusy(true);
    setErr(null);
    const ok = await onConfirm(target);
    if (!ok) {
      setErr("예약 생성에 실패했습니다. 잠시 후 다시 시도하세요.");
      setBusy(false);
    }
  }

  function onCustomSubmit() {
    const target = new Date(custom).getTime();
    if (Number.isNaN(target)) {
      setErr("올바른 날짜/시간을 입력하세요.");
      return;
    }
    void send(target);
  }

  return (
    <div className="modal-backdrop" onClick={busy ? undefined : onCancel}>
      <div className="modal-card schedule-modal" onClick={(event) => event.stopPropagation()}>
        <h3 style={{ margin: "0 0 8px" }}>🕐 메시지 예약</h3>
        <div className="schedule-target">
          <span className="channel-hash">#</span>{channelName || "채널"}
        </div>
        <div className="schedule-preview">{messagePreview}</div>
        <div className="schedule-presets">
          <button type="button" className="btn-ghost" disabled={busy} onClick={() => void send(Date.now() + 3_600_000)}>1시간 후</button>
          <button type="button" className="btn-ghost" disabled={busy} onClick={() => void send(tomorrow9am())}>내일 오전 9시</button>
          <button type="button" className="btn-ghost" disabled={busy} onClick={() => void send(nextMonday9am())}>다음 주 월요일 오전 9시</button>
        </div>
        <div className="schedule-custom">
          <label>사용자 지정</label>
          <input
            type="datetime-local"
            className="field-input"
            value={custom}
            onChange={(event) => setCustom(event.target.value)}
          />
          <button
            type="button"
            className="btn-primary"
            style={{ width: "auto", padding: "0 14px", height: 36 }}
            onClick={onCustomSubmit}
            disabled={busy}
          >{busy ? "예약 중…" : "예약"}</button>
        </div>
        {err && <div className="login-error">{err}</div>}
        <div style={{ display: "flex", justifyContent: "flex-end", marginTop: 10 }}>
          <button
            type="button"
            className="btn-ghost"
            style={{ width: "auto", padding: "0 14px", height: 34 }}
            onClick={onCancel}
            disabled={busy}
          >닫기</button>
        </div>
      </div>
    </div>
  );
}

type ReminderPopoverProps = {
  postId: string;
  onCancel: () => void;
  onConfirm: (postId: string, remindAtMs: number) => Promise<boolean>;
};

export function ReminderPopover({ postId, onCancel, onConfirm }: ReminderPopoverProps) {
  useEscClose(true, onCancel);
  const [custom, setCustom] = useState<string>(() => {
    const d = new Date(Date.now() + 60 * 60 * 1000);
    d.setSeconds(0, 0);
    const pad = (n: number) => n.toString().padStart(2, "0");
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
  });
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  function tomorrow9am(): number {
    const d = new Date();
    d.setDate(d.getDate() + 1);
    d.setHours(9, 0, 0, 0);
    return d.getTime();
  }

  function nextMonday9am(): number {
    const d = new Date();
    const delta = ((1 - d.getDay() + 7) % 7) || 7;
    d.setDate(d.getDate() + delta);
    d.setHours(9, 0, 0, 0);
    return d.getTime();
  }

  async function send(target: number) {
    if (busy) return;
    if (target <= Date.now() - 30_000) {
      setErr("미래 시각을 선택하세요.");
      return;
    }
    setBusy(true);
    setErr(null);
    const ok = await onConfirm(postId, target);
    if (!ok) {
      setErr("리마인더 생성에 실패했습니다.");
      setBusy(false);
    }
  }

  function onCustomSubmit() {
    const target = new Date(custom).getTime();
    if (Number.isNaN(target)) {
      setErr("올바른 날짜/시간을 입력하세요.");
      return;
    }
    void send(target);
  }

  return (
    <div className="modal-backdrop" onClick={busy ? undefined : onCancel}>
      <div className="modal-card reminder-popover" onClick={(event) => event.stopPropagation()}>
        <h3 style={{ margin: "0 0 10px" }}>🔔 리마인더 설정</h3>
        <div className="schedule-presets">
          <button type="button" className="btn-ghost" disabled={busy} onClick={() => void send(Date.now() + 30 * 60_000)}>30분 후</button>
          <button type="button" className="btn-ghost" disabled={busy} onClick={() => void send(Date.now() + 60 * 60_000)}>1시간 후</button>
          <button type="button" className="btn-ghost" disabled={busy} onClick={() => void send(tomorrow9am())}>내일 오전 9시</button>
          <button type="button" className="btn-ghost" disabled={busy} onClick={() => void send(nextMonday9am())}>다음 주 월요일 오전 9시</button>
        </div>
        <div className="schedule-custom">
          <label>사용자 지정</label>
          <input
            type="datetime-local"
            className="field-input"
            value={custom}
            onChange={(event) => setCustom(event.target.value)}
          />
          <button
            type="button"
            className="btn-primary"
            style={{ width: "auto", padding: "0 14px", height: 36 }}
            onClick={onCustomSubmit}
            disabled={busy}
          >{busy ? "설정 중…" : "설정"}</button>
        </div>
        {err && <div className="login-error">{err}</div>}
        <div style={{ display: "flex", justifyContent: "flex-end", marginTop: 10 }}>
          <button
            type="button"
            className="btn-ghost"
            style={{ width: "auto", padding: "0 14px", height: 34 }}
            onClick={onCancel}
            disabled={busy}
          >닫기</button>
        </div>
      </div>
    </div>
  );
}
