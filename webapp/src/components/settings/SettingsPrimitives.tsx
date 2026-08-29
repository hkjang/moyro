import { Alert, Box, Button, CircularProgress, Paper, Stack, Typography } from "@mui/material";

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
      <Stack direction={{ xs: "column", sm: "row" }} sx={{ gap: 2, justifyContent: "space-between" }}>
        <Box>
          <Typography component="h1" variant="h3">{title}</Typography>
          <Typography color="text.secondary" sx={{ mt: 0.75 }}>{description}</Typography>
        </Box>
        {actions && <Stack direction="row" sx={{ alignItems: "flex-start", gap: 1 }}>{actions}</Stack>}
      </Stack>
      {children}
    </Stack>
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
    <Paper variant="outlined" sx={{ p: { xs: 2, sm: 3 }, borderColor: "divider" }}>
      <Typography component="h2" variant="h5">{title}</Typography>
      {description && (
        <Typography color="text.secondary" variant="body2" sx={{ mt: 0.5, mb: 2.5 }}>
          {description}
        </Typography>
      )}
      {!description && <Box sx={{ mb: 2 }} />}
      {children}
    </Paper>
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

export function SaveBar({
  saving,
  saved,
  onSave,
  label = "설정 저장",
}: {
  saving: boolean;
  saved: string;
  onSave: () => void;
  label?: string;
}) {
  return (
    <Stack direction="row" sx={{ gap: 1.5, alignItems: "center", justifyContent: "flex-end" }}>
      {saved && <Typography color="success.main" variant="body2" role="status">{saved}</Typography>}
      <Button variant="contained" onClick={onSave} disabled={saving}>
        {saving ? "저장 중…" : label}
      </Button>
    </Stack>
  );
}
