import { redirect } from "next/navigation";
import { paths } from "@multica/core/paths";

/**
 * PUL-238 — server-side default-landing redirect.
 *
 * Visiting `/[workspaceSlug]` directly now lands on Mission Control
 * (the workspace-level action inbox shipped in PUL-231 PR2). The
 * redirect lives in a Server Component so it runs on the edge before
 * any client JS executes — no flash of the wrong page, no extra
 * hydration cost. Push-notification deeplinks like
 * `/[workspaceSlug]/issues/<id>` resolve to their own page.tsx files
 * and don't hit this redirect at all.
 *
 * Opt-out: set `NEXT_PUBLIC_DISABLE_MISSION_CONTROL_REDIRECT=true` at
 * build time and the dashboard root falls back to the legacy `issues`
 * landing surface. Default (unset / "false") = redirect to Mission
 * Control. Single-user MVP today; per-workspace setting can land in a
 * follow-up if other users want different defaults.
 */
export default async function Page({
  params,
}: {
  params: Promise<{ workspaceSlug: string }>;
}) {
  const { workspaceSlug } = await params;
  const optOut = process.env.NEXT_PUBLIC_DISABLE_MISSION_CONTROL_REDIRECT === "true";
  const target = optOut
    ? paths.workspace(workspaceSlug).issues()
    : paths.workspace(workspaceSlug).missionControl();
  redirect(target);
}
