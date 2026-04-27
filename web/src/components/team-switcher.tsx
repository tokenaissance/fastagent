"use client";

import * as React from "react";
import { useRouter } from "next/navigation";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  useSidebar,
} from "@/components/ui/sidebar";
import { ChevronsUpDownIcon, PlusIcon } from "lucide-react";

function AgentAvatar({ size = 32 }: { size?: number }) {
  return (
    <img
      src="/logo.png"
      alt="FastClaw"
      width={size}
      height={size}
      className="rounded-lg"
      style={{ width: size, height: size }}
    />
  );
}

export interface AgentSwitcherItem {
  id: string;
  model?: string;
}

export function AgentSwitcher({
  agents,
  activeAgentId,
  onSelect,
}: {
  agents: AgentSwitcherItem[];
  activeAgentId?: string | null;
  onSelect?: (id: string) => void;
}) {
  const { isMobile } = useSidebar();
  const router = useRouter();

  const active =
    agents.find((a) => a.id === activeAgentId) ?? agents[0] ?? null;

  const goto = React.useCallback(
    (id: string) => {
      if (onSelect) onSelect(id);
      else router.push(`/agents/${id}/chat/`);
    },
    [onSelect, router],
  );

  if (!active) {
    return (
      <SidebarMenu>
        <SidebarMenuItem>
          <SidebarMenuButton
            size="lg"
            onClick={() => router.push("/agents/")}
          >
            <div className="flex aspect-square size-8 items-center justify-center rounded-lg bg-sidebar-primary text-sidebar-primary-foreground">
              <PlusIcon className="size-4" />
            </div>
            <div className="grid flex-1 text-left text-sm leading-tight">
              <span className="truncate font-medium">No agents</span>
            </div>
          </SidebarMenuButton>
        </SidebarMenuItem>
      </SidebarMenu>
    );
  }

  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <SidebarMenuButton
                size="lg"
                className="data-open:bg-sidebar-accent data-open:text-sidebar-accent-foreground"
              />
            }
          >
            <AgentAvatar size={32} />
            <div className="grid flex-1 text-left text-sm leading-tight">
              <span className="truncate font-medium">{active.id}</span>
            </div>
            <ChevronsUpDownIcon className="ml-auto" />
          </DropdownMenuTrigger>
          <DropdownMenuContent
            className="min-w-56 rounded-lg"
            align="start"
            side={isMobile ? "bottom" : "right"}
            sideOffset={4}
          >
            <DropdownMenuGroup>
              <DropdownMenuLabel className="text-xs text-muted-foreground">
                Agents
              </DropdownMenuLabel>
              {agents.map((a) => (
                <DropdownMenuItem
                  key={a.id}
                  onClick={() => goto(a.id)}
                  className="gap-2 p-2"
                >
                  <AgentAvatar size={24} />
                  <span className="flex-1 truncate">{a.id}</span>
                </DropdownMenuItem>
              ))}
            </DropdownMenuGroup>
            <DropdownMenuSeparator />
            <DropdownMenuGroup>
              <DropdownMenuItem
                className="gap-2 p-2"
                onClick={() => router.push("/agents/")}
              >
                <div className="flex size-6 items-center justify-center rounded-md border bg-transparent">
                  <PlusIcon className="size-4" />
                </div>
                <div className="font-medium text-muted-foreground">
                  Manage agents
                </div>
              </DropdownMenuItem>
            </DropdownMenuGroup>
          </DropdownMenuContent>
        </DropdownMenu>
      </SidebarMenuItem>
    </SidebarMenu>
  );
}
