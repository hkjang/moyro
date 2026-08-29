import ContentCopyRounded from "@mui/icons-material/ContentCopyRounded";
import DeleteOutlineRounded from "@mui/icons-material/DeleteOutlineRounded";
import RefreshRounded from "@mui/icons-material/RefreshRounded";
import {
  Alert,
  Button,
  Chip,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  FormControlLabel,
  FormGroup,
  Stack,
  TextField,
  Typography,
  Checkbox,
} from "@mui/material";
import { DataGrid, type GridColDef } from "@mui/x-data-grid";
import { useEffect, useMemo, useState } from "react";
import { useSelector } from "react-redux";
import { moyroMeApi, type PersonalAPIKey } from "@/api/client";
import { SettingsCard, SettingsPage } from "@/components/settings/SettingsPrimitives";
import type { RootState } from "@/store";

const DEFAULT_SCOPES = [
  "manage_own_api_keys",
  "use_ai",
  "mcp_read",
  "mcp_write",
  "request_approval",
  "review_approval",
];

export function PersonalKeysPage() {
  const token = useSelector((state: RootState) => state.auth.token);
  const [rows, setRows] = useState<PersonalAPIKey[]>([]);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [name, setName] = useState("");
  const [ttl, setTTL] = useState(90);
  const [scopes, setScopes] = useState<string[]>(["mcp_read"]);
  const [secret, setSecret] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");

  async function load() {
    if (!token) return;
    try {
      setRows(await moyroMeApi.listAPIKeys(token));
      setMessage("");
    } catch (err) {
      setMessage(err instanceof Error ? err.message : "개인 키 API에 연결하지 못했습니다.");
    }
  }

  useEffect(() => { void load(); }, [token]); // eslint-disable-line react-hooks/exhaustive-deps

  async function create() {
    if (!token || !name.trim()) return;
    setBusy(true);
    try {
      const value = await moyroMeApi.createAPIKey(token, { name: name.trim(), scopes, ttl_days: ttl });
      setSecret(value.secret);
      setRows((prev) => [value, ...prev]);
      setDialogOpen(false);
      setName("");
      setMessage("");
    } catch (err) {
      setMessage(err instanceof Error ? err.message : "키를 만들지 못했습니다.");
    } finally {
      setBusy(false);
    }
  }

  async function rotate(id: string) {
    if (!token) return;
    setBusy(true);
    try {
      const value = await moyroMeApi.rotateAPIKey(token, id);
      setSecret(value.secret);
      setRows((prev) => prev.map((row) => row.id === id ? value : row));
      setMessage("새 키가 발급되었습니다. 기존 키는 관리자가 정한 유예시간 뒤 폐기됩니다.");
    } catch (err) {
      setMessage(err instanceof Error ? err.message : "키를 회전하지 못했습니다.");
    } finally {
      setBusy(false);
    }
  }

  async function remove(id: string) {
    if (!token) return;
    setBusy(true);
    try {
      await moyroMeApi.deleteAPIKey(token, id);
      setRows((prev) => prev.filter((row) => row.id !== id));
    } catch (err) {
      setMessage(err instanceof Error ? err.message : "키를 폐기하지 못했습니다.");
    } finally {
      setBusy(false);
    }
  }

  const columns = useMemo<GridColDef<PersonalAPIKey>[]>(() => [
    { field: "name", headerName: "이름", flex: 1, minWidth: 150 },
    { field: "prefix", headerName: "Prefix", width: 130 },
    {
      field: "scopes",
      headerName: "권한",
      flex: 1.4,
      minWidth: 210,
      sortable: false,
      renderCell: ({ value }) => <Stack direction="row" sx={{ gap: 0.5, flexWrap: "wrap" }}>{(value as string[]).map((scope) => <Chip key={scope} size="small" label={scope} />)}</Stack>,
    },
    { field: "status", headerName: "상태", width: 100 },
    {
      field: "created_at",
      headerName: "생성일",
      width: 150,
      valueFormatter: (value: number) => value ? new Date(value).toLocaleDateString() : "—",
    },
    {
      field: "actions",
      headerName: "작업",
      width: 190,
      sortable: false,
      filterable: false,
      renderCell: ({ row }) => (
        <Stack direction="row" sx={{ gap: 0.5 }}>
          <Button size="small" startIcon={<RefreshRounded />} onClick={() => void rotate(row.id)} disabled={busy}>회전</Button>
          <Button size="small" color="error" startIcon={<DeleteOutlineRounded />} onClick={() => void remove(row.id)} disabled={busy}>폐기</Button>
        </Stack>
      ),
    },
  ], [busy]);

  return (
    <SettingsPage title="개인 키" description="API와 MCP client에서 사용할 내 키를 만들고 주기적으로 회전합니다." actions={<Button variant="contained" onClick={() => setDialogOpen(true)}>새 키</Button>}>
      {message && <Alert severity="warning">{message}</Alert>}
      {secret && (
        <Alert severity="success" onClose={() => setSecret("")}>
          <Stack spacing={1}>
            <Typography variant="subtitle2">이 secret은 지금 한 번만 표시됩니다.</Typography>
            <Typography component="code" className="moyro-secret-value">{secret}</Typography>
            <Button size="small" startIcon={<ContentCopyRounded />} onClick={() => void navigator.clipboard.writeText(secret)} sx={{ alignSelf: "flex-start" }}>복사</Button>
          </Stack>
        </Alert>
      )}
      <SettingsCard title={`발급된 키 ${rows.length}개`} description="목록에는 secret 대신 식별 가능한 prefix와 최근 상태만 표시합니다.">
        <DataGrid
          rows={rows}
          columns={columns}
          autoHeight
          disableRowSelectionOnClick
          pageSizeOptions={[10, 25, 50]}
          initialState={{ pagination: { paginationModel: { pageSize: 10, page: 0 } } }}
          sx={{ border: 0, "& .MuiDataGrid-cell": { alignItems: "center" } }}
        />
      </SettingsCard>

      <Dialog open={dialogOpen} onClose={() => setDialogOpen(false)} fullWidth maxWidth="sm">
        <DialogTitle>새 개인 키</DialogTitle>
        <DialogContent>
          <Stack spacing={2.25} sx={{ pt: 1 }}>
            <TextField autoFocus required label="키 이름" value={name} onChange={(event) => setName(event.target.value)} placeholder="개발 PC MCP" />
            <TextField type="number" label="유효기간 (일)" value={ttl} onChange={(event) => setTTL(Math.max(1, Number(event.target.value) || 1))} />
            <FormGroup>
              {DEFAULT_SCOPES.map((scope) => (
                <FormControlLabel
                  key={scope}
                  control={<Checkbox checked={scopes.includes(scope)} onChange={(event) => setScopes((prev) => event.target.checked ? [...prev, scope] : prev.filter((item) => item !== scope))} />}
                  label={scope}
                />
              ))}
            </FormGroup>
          </Stack>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setDialogOpen(false)}>취소</Button>
          <Button variant="contained" onClick={() => void create()} disabled={busy || !name.trim() || scopes.length === 0}>생성</Button>
        </DialogActions>
      </Dialog>
    </SettingsPage>
  );
}
