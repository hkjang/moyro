import { useId } from "react";
import {
  Alert,
  Box,
  Button,
  Chip,
  CircularProgress,
  Paper,
  Stack,
  Typography,
} from "@mui/material";

export type PageHeaderProps = {
  title: string;
  description?: string;
  eyebrow?: string;
  actions?: React.ReactNode;
};

export function PageHeader({ title, description, eyebrow, actions }: PageHeaderProps) {
  return (
    <Stack
      component="header"
      direction={{ xs: "column", sm: "row" }}
      sx={{ gap: 2, justifyContent: "space-between" }}
    >
      <Box sx={{ minWidth: 0 }}>
        {eyebrow && (
          <Typography
            variant="caption"
            sx={{ color: "primary.main", display: "block", fontWeight: 700, mb: 0.5 }}
          >
            {eyebrow}
          </Typography>
        )}
        <Typography component="h1" variant="h3">{title}</Typography>
        {description && (
          <Typography color="text.secondary" sx={{ mt: 0.75 }}>
            {description}
          </Typography>
        )}
      </Box>
      {actions && (
        <Stack direction="row" sx={{ alignItems: "flex-start", flexWrap: "wrap", gap: 1 }}>
          {actions}
        </Stack>
      )}
    </Stack>
  );
}

export function SettingsPage({
  title,
  description,
  actions,
  children,
}: {
  title: string;
  description: string;
  actions?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <Stack spacing={3} sx={{ width: "100%", maxWidth: 1040, mx: "auto" }}>
      <PageHeader title={title} description={description} actions={actions} />
      {children}
    </Stack>
  );
}

export type FormSectionProps = {
  title: string;
  description?: string;
  actions?: React.ReactNode;
  children: React.ReactNode;
  danger?: boolean;
};

export function FormSection({ title, description, actions, children, danger = false }: FormSectionProps) {
  const headingID = useId();
  return (
    <Paper
      component="section"
      variant="outlined"
      aria-labelledby={headingID}
      sx={{
        p: { xs: 2, sm: 3 },
        borderColor: danger ? "error.main" : "divider",
      }}
    >
      <Stack direction={{ xs: "column", sm: "row" }} sx={{ gap: 1.5, justifyContent: "space-between" }}>
        <Box sx={{ minWidth: 0 }}>
          <Typography id={headingID} component="h2" variant="h5">{title}</Typography>
          {description && (
            <Typography color="text.secondary" variant="body2" sx={{ mt: 0.5 }}>
              {description}
            </Typography>
          )}
        </Box>
        {actions && (
          <Stack direction="row" sx={{ alignItems: "flex-start", flexWrap: "wrap", gap: 1 }}>
            {actions}
          </Stack>
        )}
      </Stack>
      <Box sx={{ mt: description ? 2.5 : 2 }}>{children}</Box>
    </Paper>
  );
}

export function SettingsCard({
  title,
  description,
  children,
}: {
  title: string;
  description?: string;
  children: React.ReactNode;
}) {
  return (
    <FormSection title={title} description={description}>
      {children}
    </FormSection>
  );
}

export function LoadState({
  loading,
  error,
  children,
}: {
  loading: boolean;
  error: string;
  children: React.ReactNode;
}) {
  if (loading) {
    return (
      <Stack direction="row" sx={{ alignItems: "center", gap: 1.5 }} role="status">
        <CircularProgress size={22} />
        <Typography>설정을 불러오는 중입니다.</Typography>
      </Stack>
    );
  }
  return (
    <Stack spacing={2}>
      {error && (
        <Alert severity="warning">
          {error} 입력한 값은 화면에 유지되며 API가 준비되면 다시 저장할 수 있습니다.
        </Alert>
      )}
      {children}
    </Stack>
  );
}

export type SaveBarProps = {
  saving: boolean;
  saved: string;
  onSave: () => void;
  label?: string;
};

export function SaveBar({ saving, saved, onSave, label = "설정 저장" }: SaveBarProps) {
  return (
    <Stack direction="row" sx={{ gap: 1.5, alignItems: "center", justifyContent: "flex-end" }}>
      {saved && <Typography color="success.main" variant="body2" role="status">{saved}</Typography>}
      <Button variant="contained" onClick={onSave} disabled={saving}>
        {saving ? "저장 중…" : label}
      </Button>
    </Stack>
  );
}

export type StickySaveBarProps = SaveBarProps & {
  dirty?: boolean;
  disabled?: boolean;
  onCancel?: () => void;
  cancelLabel?: string;
  lastSavedAt?: string;
};

export function StickySaveBar({
  saving,
  saved,
  onSave,
  label = "설정 저장",
  dirty,
  disabled = false,
  onCancel,
  cancelLabel = "변경 취소",
  lastSavedAt,
}: StickySaveBarProps) {
  const stateMessage = saved
    || (dirty === true ? "저장되지 않은 변경사항이 있습니다." : "")
    || (lastSavedAt ? `마지막 저장 ${lastSavedAt}` : "");

  return (
    <Paper
      component="footer"
      variant="outlined"
      sx={{
        position: "sticky",
        zIndex: 10,
        bottom: { xs: "8px", sm: "16px" },
        px: { xs: 1.5, sm: 2 },
        py: 1.25,
        borderColor: dirty ? "primary.main" : "divider",
        bgcolor: "background.paper",
        boxShadow: "0 12px 32px rgba(24, 32, 51, 0.12)",
      }}
    >
      <Stack
        direction={{ xs: "column", sm: "row" }}
        sx={{ gap: 1.5, alignItems: { sm: "center" }, justifyContent: "space-between" }}
      >
        <Typography
          color={saved ? "success.main" : dirty ? "text.primary" : "text.secondary"}
          variant="body2"
          role="status"
          aria-live="polite"
        >
          {stateMessage || "설정이 최신 상태입니다."}
        </Typography>
        <Stack direction="row" sx={{ gap: 1, justifyContent: "flex-end" }}>
          {onCancel && (
            <Button variant="outlined" onClick={onCancel} disabled={saving}>
              {cancelLabel}
            </Button>
          )}
          <Button
            variant="contained"
            onClick={onSave}
            disabled={saving || disabled || dirty === false}
          >
            {saving ? "저장 중…" : label}
          </Button>
        </Stack>
      </Stack>
    </Paper>
  );
}

export type StatusTone = "neutral" | "brand" | "ai" | "automation" | "approval" | "success" | "danger";

const statusToneStyles: Record<StatusTone, { color: string; borderColor: string }> = {
  neutral: { color: "text.secondary", borderColor: "divider" },
  brand: {
    color: "var(--moyro-palette-primary-main)",
    borderColor: "var(--moyro-palette-primary-main)",
  },
  ai: {
    color: "var(--moyro-palette-ai-main)",
    borderColor: "var(--moyro-palette-ai-main)",
  },
  automation: {
    color: "var(--moyro-palette-automation-main)",
    borderColor: "var(--moyro-palette-automation-main)",
  },
  approval: {
    color: "var(--moyro-palette-approval-main)",
    borderColor: "var(--moyro-palette-approval-main)",
  },
  success: {
    color: "var(--moyro-palette-success-main)",
    borderColor: "var(--moyro-palette-success-main)",
  },
  danger: {
    color: "var(--moyro-palette-error-main)",
    borderColor: "var(--moyro-palette-error-main)",
  },
};

export function StatusBadge({
  label,
  tone = "neutral",
  icon,
}: {
  label: React.ReactNode;
  tone?: StatusTone;
  icon?: React.ReactElement;
}) {
  return (
    <Chip
      label={label}
      icon={icon}
      size="small"
      variant="outlined"
      sx={{
        ...statusToneStyles[tone],
        borderRadius: "var(--moyro-flow-radii-pill, 999px)",
        fontWeight: 700,
      }}
    />
  );
}

export function EmptyState({
  title,
  description,
  icon,
  action,
}: {
  title: string;
  description: string;
  icon?: React.ReactNode;
  action?: React.ReactNode;
}) {
  const headingID = useId();
  return (
    <Paper
      component="section"
      variant="outlined"
      aria-labelledby={headingID}
      sx={{ px: 3, py: 4, borderStyle: "dashed", textAlign: "center" }}
    >
      <Stack spacing={1.25} sx={{ alignItems: "center", maxWidth: 520, mx: "auto" }}>
        {icon && <Box aria-hidden>{icon}</Box>}
        <Typography id={headingID} component="h2" variant="h5">{title}</Typography>
        <Typography color="text.secondary" variant="body2">{description}</Typography>
        {action && <Box sx={{ pt: 1 }}>{action}</Box>}
      </Stack>
    </Paper>
  );
}
