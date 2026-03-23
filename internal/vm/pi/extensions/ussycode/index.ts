import type {
  ExtensionAPI,
  ExtensionContext,
  ProviderModelConfig,
  BeforeAgentStartEventResult,
} from "@mariozechner/pi-coding-agent";
import { Type } from "@sinclair/typebox";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { homedir } from "node:os";
import { execSync } from "node:child_process";

interface RoutussyModel {
  id: string;
  name: string;
  metadata?: {
    tool_call?: boolean;
    reasoning?: boolean;
    display_name?: string;
    cost?: { input: number; output: number; cache_read: number; cache_write: number };
    limit?: { context: number; output: number };
    modalities?: { input: string[]; output: string[] };
  };
}

interface BudgetInfo {
  budget_cents: number;
  spent_cents: number;
  fetchedAt: number;
}

interface TextContent {
  type: "text";
  text: string;
}

const MARKER_FILE = join(homedir(), ".ussycode-onboarded");
const BUDGET_CACHE_TTL_MS = 5 * 60 * 1000;

const FALLBACK_MODELS: ProviderModelConfig[] = [
  {
    id: "glm-4.5",
    name: "GLM-4.5",
    reasoning: true,
    input: ["text"] as ("text" | "image")[],
    cost: { input: 0.6, output: 2.2, cacheRead: 0.15, cacheWrite: 0.6 },
    contextWindow: 128000,
    maxTokens: 16384,
  },
  {
    id: "glm-4.5-flash",
    name: "GLM-4.5-Flash",
    reasoning: false,
    input: ["text"] as ("text" | "image")[],
    cost: { input: 0.2, output: 0.8, cacheRead: 0.05, cacheWrite: 0.2 },
    contextWindow: 128000,
    maxTokens: 8192,
  },
  {
    id: "glm-5-turbo",
    name: "GLM-5-Turbo",
    reasoning: true,
    input: ["text"] as ("text" | "image")[],
    cost: { input: 0.8, output: 3.0, cacheRead: 0.2, cacheWrite: 0.8 },
    contextWindow: 128000,
    maxTokens: 16384,
  },
];

function stripV1(url: string): string {
  return url.replace(/\/v1\/?$/, "");
}

function getVmName(): string {
  if (process.env.USSYCODE_VM_NAME) return process.env.USSYCODE_VM_NAME;
  if (process.env.USSYCODE_VM) return process.env.USSYCODE_VM;
  try {
    return execSync("hostname", { encoding: "utf8" }).trim();
  } catch {
    return "unknown-vm";
  }
}

function getUserHandle(): string {
  return process.env.USSYCODE_USER ?? "user";
}

function getPublicUrl(): string {
  const vmName = getVmName();
  const publicDomain = process.env.USSYCODE_PUBLIC_DOMAIN?.trim();
  if (publicDomain) return `https://${vmName}.${publicDomain}`;
  return `https://${vmName}-${getUserHandle()}.ussyco.de`;
}

function extractFingerprint(apiKey: string): string | null {
  const match = apiKey.match(/^ussycode-fp:(.+)$/);
  return match ? match[1] : null;
}

function telemetry(event: string, props: Record<string, unknown> = {}): void {
  try {
    console.log(
      JSON.stringify({
        ts: new Date().toISOString(),
        event,
        vm: getVmName(),
        user: getUserHandle(),
        ...props,
      })
    );
  } catch {
    // never crash on telemetry
  }
}

function textResult<T>(data: T): { content: TextContent[]; details: T } {
  return {
    content: [{ type: "text" as const, text: JSON.stringify(data, null, 2) }],
    details: data,
  };
}

async function fetchModels(baseUrl: string, apiKey: string): Promise<ProviderModelConfig[]> {
  const resp = await fetch(`${baseUrl}/v1/models`, {
    headers: { Authorization: `Bearer ${apiKey}` },
    signal: AbortSignal.timeout(5000),
  });
  if (!resp.ok) throw new Error(`Model fetch failed: ${resp.status}`);
  const data = (await resp.json()) as { data: RoutussyModel[] };
  return data.data
    .filter((m: RoutussyModel) => m.metadata?.tool_call === true)
    .map(
      (m: RoutussyModel): ProviderModelConfig => ({
        id: m.id,
        name: m.metadata?.display_name || m.name || m.id,
        reasoning: m.metadata?.reasoning ?? false,
        input: (m.metadata?.modalities?.input ?? ["text"]).includes("image")
          ? (["text", "image"] as ("text" | "image")[])
          : (["text"] as ("text" | "image")[]),
        cost: {
          input: m.metadata?.cost?.input ?? 0,
          output: m.metadata?.cost?.output ?? 0,
          cacheRead: m.metadata?.cost?.cache_read ?? 0,
          cacheWrite: m.metadata?.cost?.cache_write ?? 0,
        },
        contextWindow: m.metadata?.limit?.context ?? 128000,
        maxTokens: m.metadata?.limit?.output ?? 8192,
      })
    );
}

let _budgetCache: BudgetInfo | null = null;

async function fetchBudget(baseUrl: string, apiKey: string): Promise<BudgetInfo | null> {
  const now = Date.now();
  if (_budgetCache && now - _budgetCache.fetchedAt < BUDGET_CACHE_TTL_MS) {
    return _budgetCache;
  }

  const fp = extractFingerprint(apiKey);
  if (!fp) return null;

  const t0 = Date.now();
  try {
    const resp = await fetch(
      `${baseUrl}/ussycode/user-by-fingerprint?fingerprint=${encodeURIComponent(fp)}`,
      {
        headers: { Authorization: `Bearer ${apiKey}` },
        signal: AbortSignal.timeout(5000),
      }
    );
    const latency = Date.now() - t0;

    if (!resp.ok) {
      telemetry("ussycode.pi.budget.fetch", { ok: false, status: resp.status, latency_ms: latency });
      return null;
    }

    const data = (await resp.json()) as { budget_cents: number; spent_cents: number };
    _budgetCache = {
      budget_cents: data.budget_cents ?? 0,
      spent_cents: data.spent_cents ?? 0,
      fetchedAt: now,
    };
    telemetry("ussycode.pi.budget.fetch", { ok: true, latency_ms: latency });
    return _budgetCache;
  } catch (err) {
    const latency = Date.now() - t0;
    telemetry("ussycode.pi.budget.fetch", { ok: false, error: String(err), latency_ms: latency });
    return null;
  }
}

function formatCents(cents: number): string {
  return `$${(cents / 100).toFixed(2)}`;
}

function budgetStatus(info: BudgetInfo | null): string {
  if (!info) return "budget: unavailable";
  const remaining = info.budget_cents - info.spent_cents;
  return `budget: ${formatCents(remaining)} / ${formatCents(info.budget_cents)} remaining`;
}

async function isOnboarded(): Promise<boolean> {
  try {
    await readFile(MARKER_FILE, "utf8");
    return true;
  } catch {
    return false;
  }
}

async function writeOnboardedMarker(): Promise<void> {
  const content = JSON.stringify({
    onboarded_at: new Date().toISOString(),
    version: "0.1.0",
  });
  await writeFile(MARKER_FILE, content, "utf8");
}

function isPortListening(port: number): boolean {
  try {
    const out = execSync(
      `lsof -i :${port} -sTCP:LISTEN -P -n 2>/dev/null || ss -tlnp 2>/dev/null | grep :${port}`,
      { encoding: "utf8", timeout: 3000 }
    );
    return out.trim().length > 0;
  } catch {
    return false;
  }
}

export default function (pi: ExtensionAPI) {
  const rawBaseUrl = process.env.OPENCODE_BASE_URL ?? "https://api.ussyco.de/v1";
  const baseUrl = stripV1(rawBaseUrl);
  const apiKey = process.env.OPENCODE_API_KEY ?? "";

  let _lastCtx: ExtensionContext | null = null;
  let statusRefreshTimer: ReturnType<typeof setTimeout> | null = null;

  if (apiKey) {
    (async () => {
      let models: ProviderModelConfig[] = FALLBACK_MODELS;
      try {
        const fetched = await fetchModels(baseUrl, apiKey);
        if (fetched.length > 0) models = fetched;
      } catch (err) {
        console.error("[ussycode] Model fetch failed, using fallback models:", err);
      }

      pi.registerProvider("ussyrouter", {
        api: "openai-completions",
        baseUrl,
        apiKey,
        models,
      });
    })().catch((err) => {
      console.error("[ussycode] Failed to register provider:", err);
      try {
        pi.registerProvider("ussyrouter", {
          api: "openai-completions",
          baseUrl,
          apiKey,
          models: FALLBACK_MODELS,
        });
      } catch {
        // nothing we can do
      }
    });
  } else {
    console.warn("[ussycode] No OPENCODE_API_KEY set — ussyrouter provider not registered. LLM features require a valid API key.");
  }

  pi.registerTool({
    name: "ussycode_status",
    label: "ussycode status",
    description:
      "Returns status information about this ussycode VM: name, public URL, budget summary, and web port.",
    parameters: Type.Object({}),
    async execute(_toolCallId, _params, _signal, _onUpdate, _ctx) {
      const budget = await fetchBudget(baseUrl, apiKey).catch(() => null);
      const remaining = budget ? budget.budget_cents - budget.spent_cents : null;
      const data = {
        vm_name: getVmName(),
        user: getUserHandle(),
        public_url: getPublicUrl(),
        web_port: 8080,
        budget_remaining_cents: remaining,
        budget_total_cents: budget?.budget_cents ?? null,
        budget_remaining_display: budget ? formatCents(remaining!) : "unavailable",
        budget_total_display: budget ? formatCents(budget.budget_cents) : "unavailable",
      };
      return textResult(data);
    },
  });

  pi.registerTool({
    name: "ussycode_budget",
    label: "ussycode budget",
    description: "Returns a detailed breakdown of this ussycode VM's AI usage budget.",
    parameters: Type.Object({}),
    async execute(_toolCallId, _params, _signal, _onUpdate, _ctx) {
      const budget = await fetchBudget(baseUrl, apiKey).catch(() => null);
      if (!budget) {
        return textResult({
          available: false,
          message:
            "Budget information is unavailable. OPENCODE_API_KEY may not be set or the ussyrouter is unreachable.",
        });
      }
      const remaining = budget.budget_cents - budget.spent_cents;
      const percentUsed =
        budget.budget_cents > 0 ? Math.round((budget.spent_cents / budget.budget_cents) * 100) : 0;
      return textResult({
        available: true,
        total_cents: budget.budget_cents,
        spent_cents: budget.spent_cents,
        remaining_cents: remaining,
        total_display: formatCents(budget.budget_cents),
        spent_display: formatCents(budget.spent_cents),
        remaining_display: formatCents(remaining),
        percent_used: percentUsed,
        percent_remaining: 100 - percentUsed,
      });
    },
  });

  pi.registerTool({
    name: "ussycode_publish",
    label: "ussycode publish",
    description:
      "Checks if a web service is running on the specified port (default 8080) and returns the public URL for that service.",
    parameters: Type.Object({
      port: Type.Optional(Type.Number({ description: "Port to check (default: 8080)" })),
    }),
    async execute(_toolCallId, params, _signal, _onUpdate, _ctx) {
      const port = (params as { port?: number }).port ?? 8080;
      const listening = isPortListening(port);
      const publicUrl = getPublicUrl();

      if (listening) {
        return textResult({
          listening: true,
          port,
          public_url: publicUrl,
          message: `Service is running on port ${port}. Public URL: ${publicUrl}`,
        });
      }

      return textResult({
        listening: false,
        port,
        public_url: publicUrl,
        message: `No service detected on port ${port}. Start your server bound to 0.0.0.0:${port} to expose it at ${publicUrl}`,
        binding_advice:
          "Make sure your server binds to 0.0.0.0 (not localhost or 127.0.0.1) and uses port 8080.",
      });
    },
  });

  pi.registerCommand("publish", {
    description: "Check the public URL for this VM's web service",
    handler: async () => {
      try {
        pi.sendUserMessage(
          "Check if there's a web service running on port 8080 using the `ussycode_publish` tool and report the public URL. If nothing is running, briefly explain how to start a server."
        );
      } catch (err) {
        console.error("[ussycode] /publish command failed:", err);
      }
    },
  });

  pi.registerCommand("usage", {
    description: "Show AI budget usage summary",
    handler: async (_args, ctx) => {
      try {
        const budget = await fetchBudget(baseUrl, apiKey).catch(() => null);
        if (!budget) {
          ctx.ui.notify("Budget information is currently unavailable.", "warning");
          return;
        }
        const remaining = budget.budget_cents - budget.spent_cents;
        const percentUsed =
          budget.budget_cents > 0
            ? Math.round((budget.spent_cents / budget.budget_cents) * 100)
            : 0;
        const msg = [
          "💰 Budget Usage",
          `  Total:     ${formatCents(budget.budget_cents)}`,
          `  Spent:     ${formatCents(budget.spent_cents)} (${percentUsed}%)`,
          `  Remaining: ${formatCents(remaining)}`,
        ].join("\n");
        ctx.ui.notify(msg, "info");
      } catch (err) {
        console.error("[ussycode] /usage command failed:", err);
      }
    },
  });

  async function refreshStatus(ctx: ExtensionContext): Promise<void> {
    try {
      const budget = await fetchBudget(baseUrl, apiKey).catch(() => null);
      const vmName = getVmName();
      const publicUrl = getPublicUrl();
      const bStatus = budgetStatus(budget);
      ctx.ui.setStatus("ussycode", `${vmName} · ${publicUrl} · ${bStatus}`);
    } catch {
      // never crash
    }
  }

  pi.on("session_start", async (_event, ctx) => {
    try {
      _lastCtx = ctx;
      telemetry("ussycode.pi.session_start");
      await refreshStatus(ctx);

      const onboarded = await isOnboarded();
      if (!onboarded && ctx.hasUI) {
        telemetry("ussycode.pi.onboarding.started");
        try {
          const vmName = getVmName();
          const user = getUserHandle();
          const publicUrl = getPublicUrl();
          const budget = await fetchBudget(baseUrl, apiKey).catch(() => null);
          const bLine = budgetStatus(budget);

          const welcomeBody = [
            "🚀 Welcome to ussycode",
            `VM: ${vmName}`,
            `User: ${user}`,
            `URL: ${publicUrl}`,
            bLine,
            "",
            "Key tips:",
            "- Bind web servers to 0.0.0.0:8080 for public HTTPS",
            "- Use /publish to check your app URL",
            "- Use /usage to see your remaining budget",
            "- Use the ussycode_status tool when you need VM details",
          ].join("\n");

          ctx.ui.notify(welcomeBody, "info");
          await writeOnboardedMarker();
          telemetry("ussycode.pi.onboarding.completed");
        } catch (err) {
          telemetry("ussycode.pi.onboarding.skipped", { reason: String(err) });
          try {
            await writeOnboardedMarker();
          } catch {
            // ignore
          }
        }
      }
    } catch (err) {
      console.error("[ussycode] session_start handler error:", err);
    }
  });

  pi.on("before_agent_start", async (event): Promise<BeforeAgentStartEventResult | void> => {
    try {
      const onboarded = await isOnboarded();
      if (!onboarded) {
        const vmName = getVmName();
        const user = getUserHandle();
        const publicUrl = getPublicUrl();

        const injectedContent = [
          "",
          "---",
          "## ussycode Environment",
          "",
          "You are running inside an **ussycode** VM — a Firecracker microVM with persistent storage and automatic HTTPS.",
          "",
          `- VM name: ${vmName}`,
          `- User: ${user}`,
          `- Public URL: ${publicUrl}`,
          "- Web port: 8080 (auto-proxied with TLS — bind to 0.0.0.0:8080)",
          "",
          "Key rules:",
          "1. Always bind web servers to 0.0.0.0, never localhost.",
          "2. Use port 8080 for web services (the proxied port).",
          "3. Report public URLs, not localhost.",
          "4. Be efficient with token usage — the user has a limited AI budget.",
          "5. Use ussycode_status, ussycode_budget, ussycode_publish tools when relevant.",
        ].join("\n");

        return { systemPrompt: event.systemPrompt + injectedContent };
      }
    } catch (err) {
      console.error("[ussycode] before_agent_start handler error:", err);
    }
  });

  pi.on("turn_end", async (_event, ctx) => {
    try {
      _lastCtx = ctx;
      if (statusRefreshTimer) clearTimeout(statusRefreshTimer);
      statusRefreshTimer = setTimeout(() => {
        refreshStatus(ctx).catch(() => {});
        statusRefreshTimer = null;
      }, 1000);
    } catch {
      // never crash
    }
  });
}
