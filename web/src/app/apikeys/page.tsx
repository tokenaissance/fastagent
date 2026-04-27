"use client";

import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Skeleton } from "@/components/ui/skeleton";
import { KeyRound, Plus, Trash2, RefreshCw, Link2 } from "lucide-react";
import {
  listAPIKeys,
  createAPIKey,
  deleteAPIKey,
  rotateAPIKey,
  listAgentBindings,
  bindAgent,
  getAgents,
  type APIKey,
  type AgentBindings,
  type AgentDetail,
} from "@/lib/api";

export default function APIKeysPage() {
  const [keys, setKeys] = useState<APIKey[]>([]);
  const [bindings, setBindings] = useState<AgentBindings>({});
  const [agents, setAgents] = useState<AgentDetail[]>([]);
  const [loading, setLoading] = useState(true);

  const [createOpen, setCreateOpen] = useState(false);
  const [newId, setNewId] = useState("");
  const [newName, setNewName] = useState("");
  const [createBindAgents, setCreateBindAgents] = useState<Set<string>>(new Set());
  const [createError, setCreateError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  // Newly created or rotated key — shown once at top of page so user can copy.
  const [revealedToken, setRevealedToken] = useState<{ keyId: string; token: string; mode: "created" | "rotated" } | null>(null);

  const [bindKey, setBindKey] = useState<APIKey | null>(null);
  const [bindOpen, setBindOpen] = useState(false);
  // Local draft of which agents this key owns; flushed to server on Save.
  const [bindDraft, setBindDraft] = useState<Set<string>>(new Set());

  const [deleteKey, setDeleteKey] = useState<APIKey | null>(null);
  const [rotateKey, setRotateKey] = useState<APIKey | null>(null);

  const fetchAll = () => {
    setLoading(true);
    Promise.all([
      listAPIKeys(),
      listAgentBindings(),
      getAgents().catch(() => [] as AgentDetail[]),
    ])
      .then(([k, b, a]) => {
        setKeys(k);
        setBindings(b);
        setAgents(a);
      })
      .finally(() => setLoading(false));
  };

  useEffect(() => {
    fetchAll();
  }, []);

  const agentsForKey = (keyId: string): string[] =>
    Object.entries(bindings)
      .filter(([, owner]) => owner === keyId)
      .map(([agent]) => agent);

  const handleCreate = async () => {
    if (!newId.trim()) return;
    setSaving(true);
    setCreateError(null);
    try {
      const result = await createAPIKey(newId.trim(), newName.trim());
      // Bind any agents the user pre-selected. Done sequentially so a binding
      // failure (e.g. agent already owned by another key) surfaces a real
      // error message instead of silently skipping.
      for (const agentId of createBindAgents) {
        const resp = await bindAgent(agentId, result.apikey.id);
        if (!resp.ok) throw new Error(resp.error || `failed to bind ${agentId}`);
      }
      setRevealedToken({ keyId: result.apikey.id, token: result.key, mode: "created" });
      setCreateOpen(false);
      setNewId("");
      setNewName("");
      setCreateBindAgents(new Set());
      fetchAll();
    } catch (err: unknown) {
      setCreateError(err instanceof Error ? err.message : "Failed to create API key");
    }
    setSaving(false);
  };

  const toggleCreateBind = (agentId: string) => {
    setCreateBindAgents((prev) => {
      const next = new Set(prev);
      if (next.has(agentId)) next.delete(agentId);
      else next.add(agentId);
      return next;
    });
  };

  const handleDelete = async () => {
    if (!deleteKey) return;
    await deleteAPIKey(deleteKey.id);
    setDeleteKey(null);
    fetchAll();
  };

  const handleRotate = async () => {
    if (!rotateKey) return;
    const token = await rotateAPIKey(rotateKey.id);
    setRevealedToken({ keyId: rotateKey.id, token, mode: "rotated" });
    setRotateKey(null);
    fetchAll();
  };

  const openBindDialog = (k: APIKey) => {
    setBindKey(k);
    setBindDraft(new Set(agentsForKey(k.id)));
    setBindOpen(true);
  };

  const toggleBindDraft = (agentId: string) => {
    setBindDraft((prev) => {
      const next = new Set(prev);
      if (next.has(agentId)) next.delete(agentId);
      else next.add(agentId);
      return next;
    });
  };

  // Save = diff current server state against draft, fire one bindAgent per
  // change. Backend overwrites unconditionally so checking an agent owned by
  // another key just transfers ownership — explicit confirm is in the UI.
  const handleSaveBindings = async () => {
    if (!bindKey) return;
    setSaving(true);
    const before = new Set(agentsForKey(bindKey.id));
    const toBind = [...bindDraft].filter((a) => !before.has(a));
    const toUnbind = [...before].filter((a) => !bindDraft.has(a));
    for (const a of toBind) await bindAgent(a, bindKey.id);
    for (const a of toUnbind) await bindAgent(a, "");
    setSaving(false);
    setBindOpen(false);
    setBindKey(null);
    fetchAll();
  };

  return (
    <div className="p-6 space-y-6 max-w-5xl mx-auto">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-2xl font-semibold tracking-tight">API Keys</h2>
          <p className="text-sm text-muted-foreground mt-1">
            Tokens for programmatic access. Bind a key to one or more agents to scope its access.
          </p>
        </div>
        <Button onClick={() => setCreateOpen(true)}>
          <Plus className="h-4 w-4 mr-2" />
          New API Key
        </Button>
      </div>

      {revealedToken && (
        <div className="rounded-lg border border-emerald-500/30 bg-emerald-500/5 p-4">
          <p className="text-xs text-emerald-600 dark:text-emerald-400 mb-2">
            {revealedToken.mode === "created" ? "API key created" : "Token rotated"} for{" "}
            <strong>{revealedToken.keyId}</strong>. Copy this token — it won&apos;t be shown again.
          </p>
          <div className="flex items-center gap-2">
            <code className="flex-1 rounded-md bg-background border border-border px-3 py-2 font-mono text-sm break-all select-all">
              {revealedToken.token}
            </code>
            <Button variant="outline" size="sm" onClick={() => setRevealedToken(null)}>
              Dismiss
            </Button>
          </div>
        </div>
      )}

      <div className="rounded-lg border border-border bg-card">
        {loading ? (
          <div className="p-6 space-y-3">
            {[1, 2].map((i) => (
              <Skeleton key={i} className="h-14 w-full" />
            ))}
          </div>
        ) : keys.length === 0 ? (
          <div className="flex flex-col items-center justify-center py-16 text-center">
            <div className="flex h-14 w-14 items-center justify-center rounded-2xl bg-primary/10 mb-4">
              <KeyRound className="h-7 w-7 text-primary" />
            </div>
            <p className="text-sm text-muted-foreground">No API keys yet</p>
            <Button onClick={() => setCreateOpen(true)} variant="outline" className="mt-4">
              Create your first API key
            </Button>
          </div>
        ) : (
          <div className="overflow-x-auto -mx-6 px-6">
            <Table>
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead>ID</TableHead>
                  <TableHead>Token</TableHead>
                  <TableHead>Bound Agents</TableHead>
                  <TableHead>Created</TableHead>
                  <TableHead className="text-right">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {keys.map((k) => {
                  const bound = agentsForKey(k.id);
                  return (
                    <TableRow key={k.id} className="hover:bg-muted/50 transition-colors">
                      <TableCell>
                        <div className="flex items-center gap-3">
                          <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-primary/10">
                            <KeyRound className="h-4 w-4 text-primary" />
                          </div>
                          <div>
                            <div className="font-medium">{k.id}</div>
                            {k.name && (
                              <div className="text-xs text-muted-foreground">{k.name}</div>
                            )}
                          </div>
                        </div>
                      </TableCell>
                      <TableCell>
                        <code className="bg-muted px-2 py-0.5 rounded font-mono text-xs">
                          {k.key}
                        </code>
                      </TableCell>
                      <TableCell>
                        {bound.length === 0 ? (
                          <span className="text-xs text-muted-foreground italic">none</span>
                        ) : (
                          <div className="flex flex-wrap gap-1">
                            {bound.map((a) => (
                              <Badge
                                key={a}
                                variant="outline"
                                className="bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 border-emerald-500/20"
                              >
                                {a}
                              </Badge>
                            ))}
                          </div>
                        )}
                      </TableCell>
                      <TableCell>
                        <span className="text-xs text-muted-foreground">
                          {new Date(k.createdAt).toLocaleDateString()}
                        </span>
                      </TableCell>
                      <TableCell className="text-right">
                        <div className="flex items-center justify-end gap-1">
                          <Button
                            variant="ghost"
                            size="icon"
                            className="h-8 w-8 text-muted-foreground hover:text-foreground"
                            title="Bind agents"
                            onClick={() => openBindDialog(k)}
                          >
                            <Link2 className="h-3.5 w-3.5" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            className="h-8 w-8 text-muted-foreground hover:text-foreground"
                            title="Rotate token"
                            onClick={() => setRotateKey(k)}
                          >
                            <RefreshCw className="h-3.5 w-3.5" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            className="h-8 w-8 text-muted-foreground hover:text-destructive"
                            title="Delete API key"
                            onClick={() => setDeleteKey(k)}
                          >
                            <Trash2 className="h-3.5 w-3.5" />
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          </div>
        )}
      </div>

      {/* Create Dialog */}
      <Dialog
        open={createOpen}
        onOpenChange={(v) => {
          setCreateOpen(v);
          if (!v) {
            setCreateError(null);
            setNewId("");
            setNewName("");
            setCreateBindAgents(new Set());
          }
        }}
      >
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>New API Key</DialogTitle>
            <DialogDescription>
              The token will be shown once after creation. Copy it before navigating away.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 py-2">
            <div className="space-y-2">
              <Label>Key ID</Label>
              <Input
                value={newId}
                onChange={(e) => {
                  setNewId(e.target.value);
                  setCreateError(null);
                }}
                placeholder="imgany-web"
              />
              <p className="text-xs text-muted-foreground">
                Stable identifier used in agent bindings. Letters, digits, hyphens.
              </p>
            </div>
            <div className="space-y-2">
              <Label>Name (optional)</Label>
              <Input
                value={newName}
                onChange={(e) => setNewName(e.target.value)}
                placeholder="ImgAny BFF"
              />
            </div>
            <div className="space-y-2">
              <Label>Bind agents (optional)</Label>
              {agents.length === 0 ? (
                <p className="text-xs text-muted-foreground italic">
                  No agents available. You can bind later from this page.
                </p>
              ) : (
                <div className="max-h-48 overflow-y-auto space-y-1.5">
                  {agents.map((a) => {
                    const checked = createBindAgents.has(a.id);
                    const currentOwner = bindings[a.id];
                    return (
                      <div
                        key={a.id}
                        className="flex items-center justify-between gap-3 rounded-md border border-border bg-background px-3 py-2"
                      >
                        <div className="flex-1 min-w-0">
                          <div className="text-sm font-medium">{a.id}</div>
                          {currentOwner && !checked && (
                            <div className="text-xs text-amber-600 dark:text-amber-400">
                              currently owned by {currentOwner} — checking will transfer
                            </div>
                          )}
                        </div>
                        <Switch
                          checked={checked}
                          onCheckedChange={() => toggleCreateBind(a.id)}
                        />
                      </div>
                    );
                  })}
                </div>
              )}
              <p className="text-xs text-muted-foreground">
                A key with no bound agents can authenticate but has no agent access.
              </p>
            </div>
            {createError && <p className="text-xs text-destructive">{createError}</p>}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setCreateOpen(false)}>
              Cancel
            </Button>
            <Button onClick={handleCreate} disabled={!newId.trim() || saving}>
              {saving ? "Creating..." : "Create API Key"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Bind agents Dialog */}
      <Dialog open={bindOpen} onOpenChange={setBindOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <Link2 className="h-5 w-5 text-primary" />
              Bind agents to {bindKey?.id}
            </DialogTitle>
            <DialogDescription>
              Each agent can be bound to at most one API key. Checking an agent already owned by
              another key transfers ownership.
            </DialogDescription>
          </DialogHeader>
          <div className="py-2 max-h-80 overflow-y-auto space-y-2">
            {agents.length === 0 ? (
              <p className="text-sm text-muted-foreground italic">No agents available.</p>
            ) : (
              agents.map((a) => {
                const checked = bindDraft.has(a.id);
                const currentOwner = bindings[a.id];
                const ownedByOther = currentOwner && currentOwner !== bindKey?.id;
                return (
                  <div
                    key={a.id}
                    className="flex items-center justify-between gap-3 rounded-md border border-border bg-background px-3 py-2 hover:bg-muted/50"
                  >
                    <div className="flex-1 min-w-0">
                      <div className="text-sm font-medium">{a.id}</div>
                      {ownedByOther && !checked && (
                        <div className="text-xs text-amber-600 dark:text-amber-400">
                          currently owned by {currentOwner}
                        </div>
                      )}
                    </div>
                    <Switch checked={checked} onCheckedChange={() => toggleBindDraft(a.id)} />
                  </div>
                );
              })
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setBindOpen(false)}>
              Cancel
            </Button>
            <Button onClick={handleSaveBindings} disabled={saving}>
              {saving ? "Saving..." : "Save Bindings"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Rotate Confirmation */}
      <AlertDialog open={!!rotateKey} onOpenChange={() => setRotateKey(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Rotate token for {rotateKey?.id}?</AlertDialogTitle>
            <AlertDialogDescription>
              The current token stops working immediately. Any client still using it will start
              receiving 401s. The new token is shown once.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={handleRotate}>Rotate</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Delete Confirmation */}
      <AlertDialog open={!!deleteKey} onOpenChange={() => setDeleteKey(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete API key</AlertDialogTitle>
            <AlertDialogDescription>
              Delete <strong>{deleteKey?.id}</strong>? Any agent bound to this key becomes
              inaccessible to clients using its token. The bindings record is preserved — to
              free those agents, edit their bindings explicitly.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDelete}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
