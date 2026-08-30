import AddTaskRounded from "@mui/icons-material/AddTaskRounded";
import AppsRounded from "@mui/icons-material/AppsRounded";
import AutoAwesomeRounded from "@mui/icons-material/AutoAwesomeRounded";
import ChatBubbleOutlineRounded from "@mui/icons-material/ChatBubbleOutlineRounded";
import CloseRounded from "@mui/icons-material/CloseRounded";
import CommandRounded from "@mui/icons-material/KeyboardCommandKeyRounded";
import DashboardCustomizeRounded from "@mui/icons-material/DashboardCustomizeRounded";
import FactCheckRounded from "@mui/icons-material/FactCheckRounded";
import InboxRounded from "@mui/icons-material/InboxRounded";
import ManageAccountsRounded from "@mui/icons-material/ManageAccountsRounded";
import MoreHorizRounded from "@mui/icons-material/MoreHorizRounded";
import SearchRounded from "@mui/icons-material/SearchRounded";
import SettingsRounded from "@mui/icons-material/SettingsRounded";
import TodayRounded from "@mui/icons-material/TodayRounded";
import {
  Box,
  Divider,
  Dialog,
  DialogContent,
  DialogTitle,
  Drawer,
  IconButton,
  List,
  ListItemButton,
  ListItemIcon,
  ListItemText,
  TextField,
  Tooltip,
  Typography,
} from "@mui/material";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useSelector } from "react-redux";
import { Outlet, useLocation, useNavigate } from "react-router-dom";
import { BrandMark } from "@/components/brand/BrandMark";
import { useAdminAccess } from "@/features/admin/AdminAccessContext";
import type { RootState } from "@/store";
import "@/features/shell/product-shell.css";

type ShellItem = {
  to: string;
  label: string;
  icon: ReactNode;
  accent?: "default" | "approval" | "ai" | "automation";
};

const primaryItems: readonly ShellItem[] = [
  { to: "/today", label: "오늘", icon: <TodayRounded /> },
  { to: "/inbox", label: "알림함", icon: <InboxRounded /> },
  { to: "/workspace", label: "대화", icon: <ChatBubbleOutlineRounded /> },
  { to: "/my-work", label: "내 업무", icon: <AddTaskRounded />, accent: "automation" },
  { to: "/search", label: "검색", icon: <SearchRounded /> },
];

function routeIsActive(pathname: string, to: string): boolean {
  if (to === "/workspace") return pathname === to || pathname.startsWith(`${to}/`);
  return pathname === to || pathname.startsWith(`${to}/`);
}

function RailLink({ item, mobile = false, onNavigate }: {
  item: ShellItem;
  mobile?: boolean;
  onNavigate?: () => void;
}) {
  const location = useLocation();
  const navigate = useNavigate();
  const active = routeIsActive(location.pathname, item.to);
  const button = (
    <button
      type="button"
      className={`product-nav-item${mobile ? " product-nav-item-mobile" : ""}${active ? " is-active" : ""}`}
      data-accent={item.accent ?? "default"}
      aria-label={item.label}
      aria-current={active ? "page" : undefined}
      onClick={() => {
        navigate(item.to);
        onNavigate?.();
      }}
    >
      <span className="product-nav-icon" aria-hidden>{item.icon}</span>
      <span className="product-nav-label">{item.label}</span>
    </button>
  );
  return mobile ? button : <Tooltip title={item.label} placement="right" arrow>{button}</Tooltip>;
}

export function ProductShell() {
  const location = useLocation();
  const navigate = useNavigate();
  const user = useSelector((state: RootState) => state.auth.user);
  const access = useAdminAccess();
  const [moreOpen, setMoreOpen] = useState(false);
  const [commandOpen, setCommandOpen] = useState(false);
  const [commandQuery, setCommandQuery] = useState("");
  const contentRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    // Route changes should move the screen-reader/keyboard cursor into the
    // new work area without altering its scroll position.
    contentRef.current?.focus({ preventScroll: true });
  }, [location.pathname]);

  useEffect(() => {
    // Workspace owns a channel/user switcher on the same shortcut. Other
    // product surfaces use this product-level destination palette.
    if (location.pathname.startsWith("/workspace")) return undefined;
    const onKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        setCommandOpen(true);
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [location.pathname]);

  const secondaryItems = useMemo<ShellItem[]>(() => {
    // Approval history can remain relevant after an administrator disables
    // new approval policies. The API returns only requests visible in the
    // caller's actual team/resource scope, so the entry stays discoverable.
    const items: ShellItem[] = [
      { to: "/approvals", label: "승인", icon: <FactCheckRounded />, accent: "approval" },
    ];
    if (access.can("use_ai")) {
      items.push({ to: "/assistant", label: "AI", icon: <AutoAwesomeRounded />, accent: "ai" });
    }
    return items;
  }, [access]);

  const mobilePrimary = ["/today", "/workspace", "/inbox", "/search"]
    .map((to) => primaryItems.find((item) => item.to === to))
    .filter((item): item is ShellItem => Boolean(item));
  const moreItems: ShellItem[] = [
    primaryItems.find((item) => item.to === "/my-work")!,
    ...secondaryItems,
    { to: "/settings/profile", label: "내 설정", icon: <SettingsRounded /> },
    ...(access.hasAdminAccess
      ? [{ to: "/admin/overview", label: "서비스 관리", icon: <ManageAccountsRounded /> }]
      : []),
  ];
  const commandItems = useMemo(() => [
    ...primaryItems,
    ...secondaryItems,
    { to: "/settings/profile", label: "내 설정", icon: <SettingsRounded /> },
    ...(access.hasAdminAccess
      ? [{ to: "/admin/overview", label: "서비스 관리", icon: <ManageAccountsRounded /> }]
      : []),
  ], [access.hasAdminAccess, secondaryItems]);
  const visibleCommandItems = commandItems.filter((item) =>
    !commandQuery.trim() || item.label.toLocaleLowerCase("ko").includes(commandQuery.trim().toLocaleLowerCase("ko")),
  );

  const runCommand = (to: string) => {
    navigate(to);
    setCommandOpen(false);
    setCommandQuery("");
  };

  return (
    <Box className="product-shell" aria-keyshortcuts="Control+K Meta+K">
      <a className="flow-skip-link" href="#main-content">본문으로 건너뛰기</a>
      <aside className="product-rail" aria-label="글로벌 탐색">
        <Tooltip title="moyro · 오늘" placement="right" arrow>
          <button type="button" className="product-rail-brand" aria-label="오늘로 이동" onClick={() => navigate("/today")}>
            <BrandMark size={34} />
          </button>
        </Tooltip>
        <nav className="product-rail-nav" aria-label="주요 기능">
          {primaryItems.map((item) => <RailLink key={item.to} item={item} />)}
          <span className="product-rail-separator" aria-hidden />
          {secondaryItems.map((item) => <RailLink key={item.to} item={item} />)}
        </nav>
        <nav className="product-rail-nav product-rail-nav-bottom" aria-label="계정과 관리">
          {access.hasAdminAccess && (
            <RailLink item={{ to: "/admin/overview", label: "관리", icon: <ManageAccountsRounded /> }} />
          )}
          <RailLink item={{ to: "/settings/profile", label: "설정", icon: <SettingsRounded /> }} />
          <Tooltip title={`${user?.username ?? "사용자"} 프로필`} placement="right" arrow>
            <button
              type="button"
              className={`product-profile-button${location.pathname.startsWith("/settings") ? " is-active" : ""}`}
              aria-label={`${user?.username ?? "사용자"} 프로필과 설정`}
              onClick={() => navigate("/settings/profile")}
            >
              {(user?.username?.[0] ?? "M").toUpperCase()}
            </button>
          </Tooltip>
        </nav>
      </aside>

      <div ref={contentRef} id="main-content" className="product-content" tabIndex={-1}>
        <Outlet />
      </div>

      <nav className="product-mobile-nav" aria-label="모바일 탐색">
        {mobilePrimary.map((item) => <RailLink key={item.to} item={item} mobile />)}
        <button
          type="button"
          className={`product-nav-item product-nav-item-mobile${moreOpen ? " is-active" : ""}`}
          aria-label="더보기"
          aria-expanded={moreOpen}
          onClick={() => setMoreOpen(true)}
        >
          <span className="product-nav-icon" aria-hidden><MoreHorizRounded /></span>
          <span className="product-nav-label">더보기</span>
        </button>
      </nav>

      <Drawer
        anchor="bottom"
        open={moreOpen}
        onClose={() => setMoreOpen(false)}
        slotProps={{ paper: { className: "product-mobile-drawer" } }}
      >
        <Box className="product-mobile-drawer-header">
          <Box>
            <Typography variant="h6">더보기</Typography>
            <Typography variant="caption" color="text.secondary">업무, AI, 설정과 관리</Typography>
          </Box>
          <IconButton aria-label="더보기 닫기" onClick={() => setMoreOpen(false)}><CloseRounded /></IconButton>
        </Box>
        <Divider />
        <List aria-label="추가 기능">
          {moreItems.map((item) => (
            <ListItemButton
              key={item.to}
              selected={routeIsActive(location.pathname, item.to)}
              onClick={() => { navigate(item.to); setMoreOpen(false); }}
            >
              <ListItemIcon>{item.icon}</ListItemIcon>
              <ListItemText primary={item.label} />
            </ListItemButton>
          ))}
          <ListItemButton disabled>
            <ListItemIcon><AppsRounded /></ListItemIcon>
            <ListItemText primary="연결 앱" secondary="관리자가 허용한 앱이 표시됩니다" />
          </ListItemButton>
          <ListItemButton disabled>
            <ListItemIcon><DashboardCustomizeRounded /></ListItemIcon>
            <ListItemText primary="레이아웃 편집" secondary="후속 버전에서 제공" />
          </ListItemButton>
        </List>
      </Drawer>

      <Dialog
        open={commandOpen}
        onClose={() => { setCommandOpen(false); setCommandQuery(""); }}
        fullWidth
        maxWidth="sm"
        aria-labelledby="product-command-title"
      >
        <DialogTitle id="product-command-title" className="product-command-title">
          <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
            <CommandRounded color="primary" />
            <Box sx={{ flex: 1 }}>
              <Typography component="span" variant="h6">빠른 이동</Typography>
              <Typography component="span" variant="caption" color="text.secondary" sx={{ ml: 1 }}>
                Ctrl/⌘ + K
              </Typography>
            </Box>
            <IconButton
              aria-label="빠른 이동 닫기"
              onClick={() => { setCommandOpen(false); setCommandQuery(""); }}
            >
              <CloseRounded />
            </IconButton>
          </Box>
        </DialogTitle>
        <DialogContent dividers sx={{ p: 0 }}>
          <Box sx={{ p: 2 }}>
            <TextField
              autoFocus
              fullWidth
              label="화면 검색"
              placeholder="오늘, 승인, 설정…"
              value={commandQuery}
              onChange={(event) => setCommandQuery(event.target.value)}
              slotProps={{ htmlInput: { maxLength: 80 } }}
            />
          </Box>
          <List aria-label="빠른 이동 결과" sx={{ pt: 0, pb: 1.5 }}>
            {visibleCommandItems.map((item) => (
              <ListItemButton
                key={item.to}
                selected={routeIsActive(location.pathname, item.to)}
                onClick={() => runCommand(item.to)}
              >
                <ListItemIcon>{item.icon}</ListItemIcon>
                <ListItemText primary={item.label} secondary={item.to} />
              </ListItemButton>
            ))}
            {visibleCommandItems.length === 0 && (
              <Typography color="text.secondary" variant="body2" sx={{ px: 2, py: 3, textAlign: "center" }}>
                일치하는 화면이 없습니다.
              </Typography>
            )}
          </List>
        </DialogContent>
      </Dialog>
    </Box>
  );
}
