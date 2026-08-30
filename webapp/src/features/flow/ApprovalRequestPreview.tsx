import { Alert, Box, Stack, Typography } from "@mui/material";
import type { ApprovalPreview } from "./approval-preview";

export function ApprovalRequestPreview({
  preview,
  requesterLabel,
  teamLabel,
  targetLabel,
}: {
  preview: ApprovalPreview;
  requesterLabel: string;
  teamLabel?: string;
  targetLabel?: string;
}) {
  const effectiveTargetLabel = preview.target || targetLabel;
  const facts = [
    { label: "요청자", value: requesterLabel },
    { label: "실행 주체", value: preview.actor },
    ...(teamLabel ? [{ label: "팀", value: teamLabel }] : []),
    ...(effectiveTargetLabel ? [{ label: "대상", value: effectiveTargetLabel }] : []),
    { label: "영향", value: preview.impact },
    { label: "위험도", value: preview.riskLevel },
    { label: "정책", value: preview.policyName },
    { label: "정책 근거", value: preview.policyReason },
  ];

  return (
    <Stack component="section" spacing={1.5} sx={{ mt: 1.5 }} aria-label="구조화된 승인 요청 미리보기">
      <Box
        component="dl"
        sx={{
          m: 0,
          display: "grid",
          gridTemplateColumns: { xs: "1fr", sm: "repeat(2, minmax(0, 1fr))" },
          gap: 1,
        }}
      >
        {facts.map((fact) => (
          <Box
            component="div"
            key={fact.label}
            sx={{ minWidth: 0, p: 1.25, border: 1, borderColor: "divider", borderRadius: 1 }}
          >
            <Typography component="dt" variant="caption" color="text.secondary" sx={{ fontWeight: 700 }}>
              {fact.label}
            </Typography>
            <Typography component="dd" variant="body2" sx={{ m: 0, mt: 0.25, overflowWrap: "anywhere" }}>
              {fact.value}
            </Typography>
          </Box>
        ))}
      </Box>

      {preview.changes.length > 0 && (
        <Box component="section" aria-label="변경 내용">
          <Typography component="h4" variant="subtitle2" sx={{ mb: 0.75, fontWeight: 700 }}>
            변경 내용
          </Typography>
          <Stack spacing={1}>
            {preview.changes.map((change) => (
              <Box
                key={change.label}
                sx={{ p: 1.5, borderRadius: 1, bgcolor: "action.hover", minWidth: 0 }}
              >
                <Typography variant="caption" color="text.secondary" sx={{ fontWeight: 700 }}>
                  {change.label}
                </Typography>
                <Typography
                  variant="body2"
                  sx={{ mt: 0.5, whiteSpace: "pre-wrap", overflowWrap: "anywhere", lineHeight: 1.65 }}
                >
                  {change.value}
                </Typography>
              </Box>
            ))}
          </Stack>
        </Box>
      )}

      <Alert severity={preview.supported ? "info" : "warning"}>{preview.notice}</Alert>
    </Stack>
  );
}
