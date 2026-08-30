import CloseRounded from "@mui/icons-material/CloseRounded";
import {
  Alert,
  Box,
  Button,
  Chip,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  LinearProgress,
  Paper,
  Stack,
  Typography,
} from "@mui/material";
import type { ReactNode } from "react";
import "./flow-pages.css";

export function FlowPage({
  eyebrow,
  title,
  description,
  actions,
  children,
}: {
  eyebrow?: string;
  title: string;
  description: string;
  actions?: ReactNode;
  children: ReactNode;
}) {
  return (
    <Box component="main" className="flow-page">
      <Box component="header" className="flow-page-header">
        <Box className="flow-page-heading">
          {eyebrow && <Typography className="flow-eyebrow">{eyebrow}</Typography>}
          <Typography component="h1" className="flow-title">{title}</Typography>
          <Typography className="flow-description">{description}</Typography>
        </Box>
        {actions && <Stack direction="row" className="flow-page-actions">{actions}</Stack>}
      </Box>
      <Stack className="flow-page-content">{children}</Stack>
    </Box>
  );
}

export function FlowSection({
  title,
  description,
  action,
  children,
  id,
}: {
  title: string;
  description?: string;
  action?: ReactNode;
  children: ReactNode;
  id?: string;
}) {
  return (
    <Box component="section" className="flow-section" aria-labelledby={id}>
      <Box className="flow-section-header">
        <Box>
          <Typography component="h2" id={id} className="flow-section-title">{title}</Typography>
          {description && <Typography className="flow-section-description">{description}</Typography>}
        </Box>
        {action}
      </Box>
      {children}
    </Box>
  );
}

export function FlowCard({ children, className = "", ...props }: {
  children: ReactNode;
  className?: string;
  component?: "article" | "div";
}) {
  return (
    <Paper
      component={props.component ?? "article"}
      variant="outlined"
      className={`flow-card ${className}`.trim()}
    >
      {children}
    </Paper>
  );
}

export function FlowMetric({
  label,
  value,
  detail,
  tone = "neutral",
  onClick,
}: {
  label: string;
  value: string | number;
  detail: string;
  tone?: "neutral" | "brand" | "warning" | "success" | "ai";
  onClick?: () => void;
}) {
  const content = (
    <>
      <Typography className="flow-metric-label">{label}</Typography>
      <Typography className="flow-metric-value">{value}</Typography>
      <Typography className="flow-metric-detail">{detail}</Typography>
    </>
  );
  return (
    <Paper variant="outlined" className={`flow-metric flow-tone-${tone}`}>
      {onClick ? (
        <Button className="flow-metric-button" onClick={onClick}>{content}</Button>
      ) : content}
    </Paper>
  );
}

export function FlowLoading({ label = "불러오는 중…" }: { label?: string }) {
  return (
    <Stack direction="row" className="flow-loading" role="status" aria-live="polite">
      <CircularProgress size={20} />
      <Typography>{label}</Typography>
    </Stack>
  );
}

export function FlowInlineProgress({ label }: { label: string }) {
  return (
    <Box role="status" aria-live="polite" className="flow-inline-progress">
      <Typography variant="caption">{label}</Typography>
      <LinearProgress />
    </Box>
  );
}

export function FlowError({ message, onRetry }: { message: string; onRetry?: () => void }) {
  return (
    <Alert
      severity="error"
      action={onRetry ? <Button color="inherit" size="small" onClick={onRetry}>다시 시도</Button> : undefined}
    >
      {message}
    </Alert>
  );
}

export function FlowEmpty({
  title,
  description,
  action,
}: {
  title: string;
  description: string;
  action?: ReactNode;
}) {
  return (
    <Box className="flow-empty">
      <Typography component="h3" className="flow-empty-title">{title}</Typography>
      <Typography className="flow-empty-description">{description}</Typography>
      {action && <Box className="flow-empty-action">{action}</Box>}
    </Box>
  );
}

export function FlowPrepared({ title, description }: { title: string; description: string }) {
  return (
    <FlowCard className="flow-prepared">
      <Chip size="small" label="준비 중" variant="outlined" />
      <Box>
        <Typography component="h3" className="flow-item-title">{title}</Typography>
        <Typography className="flow-item-subtitle">{description}</Typography>
      </Box>
    </FlowCard>
  );
}

export type FlowTabOption<T extends string> = { value: T; label: string; count?: number };

function flowTabID(idPrefix: string, value: string): string {
  return `${idPrefix}-${value}-tab`;
}

function flowTabPanelID(idPrefix: string, value: string): string {
  return `${idPrefix}-${value}-panel`;
}

export function FlowTabs<T extends string>({
  value,
  options,
  onChange,
  label,
  idPrefix,
}: {
  value: T;
  options: readonly FlowTabOption<T>[];
  onChange: (value: T) => void;
  label: string;
  idPrefix: string;
}) {
  return (
    <Box role="tablist" aria-label={label} className="flow-tabs">
      {options.map((option) => {
        const selected = option.value === value;
        return (
          <Button
            key={option.value}
            id={flowTabID(idPrefix, option.value)}
            role="tab"
            aria-selected={selected}
            aria-controls={flowTabPanelID(idPrefix, option.value)}
            tabIndex={selected ? 0 : -1}
            className={`flow-tab ${selected ? "flow-tab-selected" : ""}`}
            onClick={() => onChange(option.value)}
            onKeyDown={(event) => {
              const keys = ["ArrowLeft", "ArrowRight", "Home", "End"];
              if (!keys.includes(event.key)) return;
              event.preventDefault();
              const currentIndex = options.findIndex((item) => item.value === option.value);
              const nextIndex = event.key === "Home"
                ? 0
                : event.key === "End"
                  ? options.length - 1
                  : event.key === "ArrowRight"
                    ? (currentIndex + 1) % options.length
                    : (currentIndex - 1 + options.length) % options.length;
              const next = options[nextIndex];
              if (!next) return;
              onChange(next.value);
              const buttons = event.currentTarget.parentElement?.querySelectorAll<HTMLButtonElement>("[role='tab']");
              buttons?.[nextIndex]?.focus();
            }}
          >
            {option.label}
            {option.count !== undefined && <span className="flow-tab-count">{option.count}</span>}
          </Button>
        );
      })}
    </Box>
  );
}

export function FlowTabPanel({
  idPrefix,
  value,
  active,
  children,
}: {
  idPrefix: string;
  value: string;
  active: boolean;
  children?: ReactNode;
}) {
  return (
    <Box
      id={flowTabPanelID(idPrefix, value)}
      role="tabpanel"
      aria-labelledby={flowTabID(idPrefix, value)}
      tabIndex={active ? 0 : -1}
      hidden={!active}
      className="flow-tab-panel"
    >
      {children}
    </Box>
  );
}

export function FlowConfirmDialog({
  open,
  title,
  description,
  confirmLabel,
  busy = false,
  destructive = false,
  onCancel,
  onConfirm,
}: {
  open: boolean;
  title: string;
  description: string;
  confirmLabel: string;
  busy?: boolean;
  destructive?: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  return (
    <Dialog open={open} onClose={busy ? undefined : onCancel} aria-labelledby="flow-confirm-title">
      <DialogTitle id="flow-confirm-title">{title}</DialogTitle>
      <DialogContent><Typography>{description}</Typography></DialogContent>
      <DialogActions>
        <Button onClick={onCancel} disabled={busy} startIcon={<CloseRounded />}>닫기</Button>
        <Button
          variant="contained"
          color={destructive ? "error" : "primary"}
          onClick={onConfirm}
          disabled={busy}
        >
          {busy ? "처리 중…" : confirmLabel}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

export function FlowStatusBadge({
  label,
  tone = "default",
}: {
  label: string;
  tone?: "default" | "warning" | "success" | "error" | "info";
}) {
  return <Chip size="small" label={label} color={tone} variant={tone === "default" ? "outlined" : "filled"} />;
}
