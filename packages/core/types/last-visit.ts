// PUL-239 cross-device last-visit map served by the new
// /api/last-visits endpoint pair. Mission Control hydrates the local
// useLastVisitStore from the list response on mount; each subsequent
// useLastVisitStore.mark() call POSTs the issue id to keep the
// server map current. See packages/views/inbox/hooks/use-last-visit-sync.ts.

export interface LastVisitItem {
  issue_id: string;
  last_visited_at: string;
}

export interface ListLastVisitsResponse {
  items: LastVisitItem[];
  total: number;
}
