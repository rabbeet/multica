"use client";

import type { Label } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { Check, ChevronDown, Tag, X } from "lucide-react";
import { useState } from "react";
import { LabelChip } from "../../labels/label-chip";
import { useT } from "../../i18n";

const FILTER_ITEM_CLASS =
  "group/fitem pr-1.5! [&>[data-slot=dropdown-menu-checkbox-item-indicator]]:hidden";

function HoverCheck({ checked }: { checked: boolean }) {
  return (
    <div
      className="border-input data-[selected=true]:border-primary data-[selected=true]:bg-primary data-[selected=true]:text-primary-foreground pointer-events-none size-4 shrink-0 rounded-[4px] border transition-all select-none *:[svg]:opacity-0 data-[selected=true]:*:[svg]:opacity-100 opacity-0 group-hover/fitem:opacity-100 group-focus/fitem:opacity-100 data-[selected=true]:opacity-100"
      data-selected={checked}
    >
      <Check className="size-3.5 text-current" />
    </div>
  );
}

export function LabelQuickFilters({
  labels,
  selected,
  onToggle,
  onClear,
}: {
  labels: Label[];
  selected: string[];
  onToggle: (labelId: string) => void;
  onClear: () => void;
}) {
  const { t } = useT("issues");
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const query = search.trim().toLowerCase();
  const filteredLabels = labels.filter((label) =>
    label.name.toLowerCase().includes(query),
  );
  const selectedLabels = selected
    .map((id) => labels.find((label) => label.id === id))
    .filter((label): label is Label => label !== undefined);

  return (
    <div
      role="toolbar"
      aria-label={t(($) => $.filters.quick_label_toolbar)}
      className="flex min-w-0 items-center gap-1"
    >
      <DropdownMenu
        open={open}
        onOpenChange={(nextOpen) => {
          setOpen(nextOpen);
          if (!nextOpen) setSearch("");
        }}
      >
        <DropdownMenuTrigger
          render={
            <Button
              variant="outline"
              size="sm"
              className={
                selected.length > 0
                  ? "border-primary/40 text-foreground"
                  : "text-muted-foreground"
              }
              aria-label={t(($) => $.filters.quick_label_button)}
            >
              <Tag className="size-3.5" />
              {t(($) => $.filters.quick_label_button)}
              <ChevronDown className="size-3" />
            </Button>
          }
        />
        <DropdownMenuContent align="start" className="min-w-56 p-0">
          <div className="border-b border-foreground/5 px-2 py-1.5">
            <input
              type="text"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              onKeyDown={(event) => {
                if (event.key !== "Escape") event.stopPropagation();
              }}
              aria-label={t(($) => $.filters.quick_label_search)}
              placeholder={t(($) => $.filters.placeholder)}
              className="w-full bg-transparent text-sm outline-none placeholder:text-muted-foreground"
              autoFocus
            />
          </div>
          <div className="max-h-64 overflow-y-auto p-1">
            {filteredLabels.map((label) => {
              const checked = selected.includes(label.id);
              return (
                <DropdownMenuCheckboxItem
                  key={label.id}
                  checked={checked}
                  onCheckedChange={() => onToggle(label.id)}
                  className={FILTER_ITEM_CLASS}
                >
                  <HoverCheck checked={checked} />
                  <LabelChip label={label} />
                </DropdownMenuCheckboxItem>
              );
            })}
            {filteredLabels.length === 0 && (
              <div className="px-2 py-3 text-center text-sm text-muted-foreground">
                {search
                  ? t(($) => $.filters.no_results)
                  : t(($) => $.filters.no_labels)}
              </div>
            )}
          </div>
        </DropdownMenuContent>
      </DropdownMenu>

      <div className="flex min-w-0 items-center gap-1 overflow-x-auto py-1">
        {selectedLabels.map((label) => (
          <button
            key={label.id}
            type="button"
            onClick={() => onToggle(label.id)}
            aria-label={t(($) => $.filters.quick_label_remove, {
              name: label.name,
            })}
            className="group shrink-0 rounded-full focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <LabelChip
              label={label}
              className="transition-opacity group-hover:opacity-80"
            />
          </button>
        ))}
      </div>

      {selected.length > 0 && (
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          onClick={onClear}
          aria-label={t(($) => $.filters.quick_label_clear)}
          title={t(($) => $.filters.quick_label_clear)}
          className="shrink-0 text-muted-foreground"
        >
          <X className="size-3.5" />
        </Button>
      )}
    </div>
  );
}
