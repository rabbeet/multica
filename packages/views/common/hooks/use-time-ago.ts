import { useT } from "../../i18n";

// PUL-177 hoisted from packages/views/inbox/components/inbox-list-item.tsx
// so the LastSkillChip (used in both inbox row and the SkillHistory
// panel on the issue detail page) can format the same way as the
// existing Inbox row timestamp. Returning a function rather than a
// string keeps the original call-site shape: `timeAgo(dateStr)`.
//
// i18n keys moved from `inbox.list.time.*` to `common.time.*` since
// the hook is now consumed by both the inbox and issues views. The
// inbox.json bundle still has the same `list.time.*` shape stripped
// out — see locale parity tests for the en/zh-Hans guarantee.
export function useTimeAgo() {
  const { t } = useT("common");
  return (dateStr: string): string => {
    const diff = Date.now() - new Date(dateStr).getTime();
    const minutes = Math.floor(diff / 60000);
    if (minutes < 1) return t(($) => $.time.just_now);
    if (minutes < 60) return t(($) => $.time.minutes, { count: minutes });
    const hours = Math.floor(minutes / 60);
    if (hours < 24) return t(($) => $.time.hours, { count: hours });
    const days = Math.floor(hours / 24);
    return t(($) => $.time.days, { count: days });
  };
}
