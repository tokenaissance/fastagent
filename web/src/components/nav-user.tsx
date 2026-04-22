"use client";

import * as React from "react";
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
import {
  ChevronsUpDownIcon,
  LogOutIcon,
  MoonIcon,
  SunIcon,
} from "lucide-react";
import { useTheme } from "@/components/theme-provider";
import { logout as doLogout, isLoggedIn } from "@/lib/auth";

export function NavUser({
  name = "Admin",
  subtitle = "Gateway running",
}: {
  name?: string;
  subtitle?: string;
}) {
  const { isMobile } = useSidebar();
  const { theme, toggleTheme } = useTheme();

  // Resolve login state on the client only — calling isLoggedIn() during
  // render hits localStorage, which is fine in the browser but the Base UI
  // Menu primitive cached its children on first render. If loggedIn flipped
  // after hydration (for example when the admin logs in), the children
  // array length changed and the menu threw on next open. Fix: track it in
  // state and re-mount the menu when it transitions.
  const [loggedIn, setLoggedIn] = React.useState(false);
  React.useEffect(() => {
    setLoggedIn(isLoggedIn());
  }, []);

  const initials = name.slice(0, 2).toUpperCase();

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
            <div className="flex aspect-square size-8 items-center justify-center rounded-lg bg-emerald-500/20 text-emerald-500 text-xs font-bold">
              {initials}
            </div>
            <div className="grid flex-1 text-left text-sm leading-tight">
              <span className="truncate font-medium">{name}</span>
              <span className="truncate text-xs text-muted-foreground">
                {subtitle}
              </span>
            </div>
            <ChevronsUpDownIcon className="ml-auto size-4" />
          </DropdownMenuTrigger>
          <DropdownMenuContent
            className="min-w-56 rounded-lg"
            side={isMobile ? "bottom" : "right"}
            align="end"
            sideOffset={4}
          >
            <DropdownMenuGroup>
              <DropdownMenuLabel className="p-0 font-normal">
                <div className="flex items-center gap-2 px-1 py-1.5 text-left text-sm">
                  <div className="flex aspect-square size-8 items-center justify-center rounded-lg bg-emerald-500/20 text-emerald-500 text-xs font-bold">
                    {initials}
                  </div>
                  <div className="grid flex-1 text-left text-sm leading-tight">
                    <span className="truncate font-medium">{name}</span>
                    <span className="truncate text-xs text-muted-foreground">
                      {subtitle}
                    </span>
                  </div>
                </div>
              </DropdownMenuLabel>
            </DropdownMenuGroup>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              onClick={(e) => {
                e.preventDefault();
                toggleTheme();
              }}
            >
              {theme === "dark" ? <SunIcon /> : <MoonIcon />}
              <span>{theme === "dark" ? "Light mode" : "Dark mode"}</span>
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              disabled={!loggedIn}
              onClick={() => {
                if (!loggedIn) return;
                doLogout();
                window.location.href = "/";
              }}
            >
              <LogOutIcon />
              <span>Log out</span>
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </SidebarMenuItem>
    </SidebarMenu>
  );
}
